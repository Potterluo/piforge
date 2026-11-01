// Package sandbox creates and manages the isolated execution environments that
// back one (user, project) pair.
//
// # Local provider
//
// Local is the development and e2e implementation: a sandbox is a directory on
// the host, holding one git clone (the "main checkout") under which the session
// layer creates per-branch git worktrees (PRD §6.4-B). Every command runs
// directly on the host, so it is NOT a security boundary — it exists so the
// server, the e2e suite and a laptop all behave the same way.
//
// Layout:
//
//	<root>/<userID>/<projectID>/              the sandbox directory
//	<root>/<userID>/<projectID>/repo/         the main git checkout (clone/init)
//	<root>/<userID>/<projectID>/worktrees/    one linked worktree per branch
//	<root>/_state/<userID>/<projectID>.json   small durable state file
//
// The checkout lives in repo/ rather than at the sandbox root so that linked
// worktrees are siblings of it: a worktree nested inside its own repository
// would show up as untracked noise in the main checkout's git status.
//
// # Where the remote provider plugs in
//
// A production deployment implements contracts.SandboxProvider a second time
// against Firecracker (default) or gVisor (PRD §7.7) and swaps it in at
// cmd/piforged construction time. Nothing above the interface changes. The
// contract that implementation must honour, and that Local approximates:
//
//   - State machine (PRD FR-5.1): CREATING → STARTING → READY → BUSY → IDLE →
//     PAUSED → TERMINATED, plus ERROR and MISSING. Ensure never returns a
//     sandbox in CREATING/STARTING: those states are progress reporting for the
//     UI while a microVM boots, not steady states.
//   - Idempotency: Ensure(user, project, repoURL, branch) called twice returns
//     the same ID and the same disk state. The ID must be deterministic and
//     derived from (user, project) so a restart can re-adopt a live sandbox.
//   - TTL (PRD FR-5.3): idle 15 min → snapshot then PAUSED; PAUSED for 14 days
//     → snapshot kept, sandbox reaped. Snapshot first, destroy second, never
//     kill first.
//   - Pause/Resume: Pause must capture a snapshot before releasing resources;
//     Resume restores it and returns State READY. With a memory snapshot the
//     PTY/tmux processes survive, which is what keeps `npm run dev` alive
//     across a pause (PRD §7.5).
//   - Snapshot hooks: filesystem `fs` and `fs+mem` tiers, bound to
//     (commit, content_hash), only ever taken at a tool boundary — never while
//     a bash tool call is executing.
//   - Exec: runs argv (never a shell string) with cwd inside the sandbox, and
//     must honour context cancellation, returning partial output.
//   - Destroy: idempotent, refuses to delete anything outside the provider
//     root, and leaves user data readable per the retention rules (§8.3).
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// Errors returned by the local provider.
var (
	// ErrNotFound is returned when a sandbox id is unknown.
	ErrNotFound = errors.New("sandbox: not found")
	// ErrInvalidID is returned for empty or path-like user/project ids.
	ErrInvalidID = errors.New("sandbox: invalid identifier")
	// ErrDestroyRefused is returned when a destroy target is outside the root.
	ErrDestroyRefused = errors.New("sandbox: refusing to destroy outside the provider root")
	// ErrPaused is returned by Exec on a paused sandbox.
	ErrPaused = errors.New("sandbox: paused")
	// ErrGone is returned when the sandbox directory vanished underneath us.
	ErrGone = errors.New("sandbox: directory is gone")
)

const (
	// ProviderName is the value reported in contracts.Sandbox.Provider.
	ProviderName = "local"
	// DefaultInitialBranch is used when LocalConfig.InitialBranch is empty.
	DefaultInitialBranch = "main"
	// DefaultExecTimeout bounds a single Exec when the caller has no deadline.
	DefaultExecTimeout = 2 * time.Minute
	// stateDirName holds the durable state files, outside the sandboxes.
	stateDirName = "_state"
	// repoSubdir is the main git checkout inside a sandbox directory.
	repoSubdir = "repo"
)

// LocalConfig configures the local provider.
type LocalConfig struct {
	// Root is the data directory that holds every sandbox. Required.
	Root string
	// GitBinary overrides git (default "git").
	GitBinary string
	// GitTimeout bounds one git invocation during Ensure (default 2 min).
	GitTimeout time.Duration
	// ExecTimeout bounds one Exec call (default 2 min).
	ExecTimeout time.Duration
	// InitialBranch is the branch created by `git init` (default "main").
	InitialBranch string
	// Node is reported in contracts.Sandbox.Node (default: hostname).
	Node string
	// Image is reported in contracts.Sandbox.Image (default "local").
	Image string
	// URLPolicy is the outbound-URL policy a clone target is checked against.
	//
	// This provider runs `git clone` on this machine, so a repo_url is a URL
	// dialled from the control plane's own network position — the same act the
	// MCP broker and the provider probe perform, and the same rule applies
	// (internal/urlguard, docs/SANDBOX-DEPLOY.md §1.9). Nil leaves the check to
	// the caller: a hand-built provider (tests, a laptop) keeps the old
	// behaviour, and cmd/piforged always wires the deployment's policy.
	URLPolicy *urlguard.Policy
}

func (c LocalConfig) normalise() LocalConfig {
	if c.GitTimeout <= 0 {
		c.GitTimeout = 2 * time.Minute
	}
	if c.ExecTimeout <= 0 {
		c.ExecTimeout = DefaultExecTimeout
	}
	if strings.TrimSpace(c.InitialBranch) == "" {
		c.InitialBranch = DefaultInitialBranch
	}
	if strings.TrimSpace(c.Image) == "" {
		c.Image = "local"
	}
	if strings.TrimSpace(c.Node) == "" {
		if h, err := os.Hostname(); err == nil {
			c.Node = h
		} else {
			c.Node = "localhost"
		}
	}
	return c
}

// Local is the host-directory implementation of contracts.SandboxProvider.
type Local struct {
	cfg LocalConfig
	git *gitx.Runner

	mu      sync.Mutex
	known   map[string]*contracts.Sandbox // by id
	keyLock sync.Map                      // id -> *sync.Mutex
}

var _ contracts.SandboxProvider = (*Local)(nil)

// NewLocal builds the local provider and verifies that Root is usable.
func NewLocal(cfg LocalConfig) (*Local, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("sandbox: LocalConfig.Root is required")
	}
	cfg = cfg.normalise()
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: abs root %q: %w", cfg.Root, err)
	}
	cfg.Root = filepath.Clean(root)
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create root %q: %w", cfg.Root, err)
	}
	gitOpts := []gitx.Option{gitx.WithTimeout(cfg.GitTimeout)}
	if cfg.GitBinary != "" {
		gitOpts = append(gitOpts, gitx.WithBinary(cfg.GitBinary))
	}
	return &Local{
		cfg:   cfg,
		git:   gitx.NewRunner(gitOpts...),
		known: make(map[string]*contracts.Sandbox),
	}, nil
}

// Root reports the configured provider root.
func (l *Local) Root() string { return l.cfg.Root }

// RootDir reports the on-disk directory of a sandbox (its "home").
func (l *Local) RootDir(sandboxID string) (string, error) {
	sb, err := l.Get(context.Background(), sandboxID)
	if err != nil {
		return "", err
	}
	if sb.RootDir == "" {
		return "", fmt.Errorf("%w: %s", ErrGone, sandboxID)
	}
	return sb.RootDir, nil
}

// RepoDir reports the main git checkout of a sandbox: <sandbox>/repo. This is
// the directory the session layer passes to gitx.AddWorktree.
func (l *Local) RepoDir(sandboxID string) (string, error) {
	dir, err := l.RootDir(sandboxID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, repoSubdir), nil
}

// WorktreePath returns (creating it) the directory where the session layer
// should place linked worktrees for this sandbox: <sandbox>/worktrees/<name>.
func (l *Local) WorktreePath(sandboxID, name string) (string, error) {
	if err := safeSegment(name); err != nil {
		return "", err
	}
	dir, err := l.RootDir(sandboxID)
	if err != nil {
		return "", err
	}
	wtRoot := filepath.Join(dir, "worktrees")
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		return "", fmt.Errorf("sandbox: create worktrees dir: %w", err)
	}
	return filepath.Join(wtRoot, name), nil
}

// ID builds the deterministic sandbox id for a (user, project) pair.
func ID(userID, projectID string) string {
	sum := sha256.Sum256([]byte(userID + "/" + projectID))
	return "sbx_" + hex.EncodeToString(sum[:8])
}

// Ensure creates (or re-adopts) the sandbox for a (user, project) pair.
//
// If the directory is missing it is created and, depending on repoURL, either
// cloned or `git init`-ed with an empty initial commit. A bare `git init` is not
// enough on its own: worktrees need a commit to branch from (PRD §6.3-坑1), so
// Ensure always leaves at least one commit behind.
func (l *Local) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	if err := safeSegment(userID); err != nil {
		return nil, err
	}
	if err := safeSegment(projectID); err != nil {
		return nil, err
	}
	id := ID(userID, projectID)
	key := l.keyMutex(id)
	key.Lock()
	defer key.Unlock()

	l.mu.Lock()
	if sb, ok := l.known[id]; ok {
		clone := *sb
		l.mu.Unlock()
		if clone.State == contracts.SandboxTerminated {
			return nil, fmt.Errorf("%w: %s is terminated", ErrGone, id)
		}
		return l.touch(&clone), nil
	}
	l.mu.Unlock()

	sandboxDir, err := l.sandboxPath(userID, projectID)
	if err != nil {
		return nil, err
	}
	repoDir := filepath.Join(sandboxDir, repoSubdir)
	created := time.Now().UTC()

	// Re-adopt a sandbox that survived a restart.
	if st, serr := l.readState(userID, projectID); serr == nil && st != nil {
		if fi, ferr := os.Stat(sandboxDir); ferr == nil && fi.IsDir() {
			sb := st.toContract()
			sb.RootDir = sandboxDir
			// A checkout created before the identity was written still needs one:
			// the agent's first commit there is exactly the case that failed.
			if gitxIsRepo(ctx, l.git, repoDir) {
				_, _ = l.git.EnsureIdentity(ctx, repoDir)
			}
			l.adopt(sb)
			return l.touch(sb), nil
		}
		// State file without a directory: report MISSING so the caller can
		// rebuild instead of silently cloning over user data.
		sb := st.toContract()
		sb.State = contracts.SandboxMissing
		sb.Message = "sandbox directory is gone"
		l.adopt(sb)
		return sb, nil
	}

	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create %s: %w", sandboxDir, err)
	}

	if !gitxIsRepo(ctx, l.git, repoDir) {
		empty, eerr := dirIsEmpty(repoDir)
		if eerr != nil {
			return nil, eerr
		}
		if !empty {
			return nil, fmt.Errorf("sandbox: %s is not a git repository", repoDir)
		}
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			return nil, fmt.Errorf("sandbox: create %s: %w", repoDir, err)
		}
		if strings.TrimSpace(repoURL) != "" {
			if err := l.cloneRepo(ctx, userID, repoDir, repoURL); err != nil {
				return nil, err
			}
		} else {
			if err := l.git.Init(ctx, repoDir, l.cfg.InitialBranch); err != nil {
				return nil, fmt.Errorf("sandbox: %w", err)
			}
			// A bare `git init` is not enough on its own: worktrees need a
			// commit to branch from (PRD §6.3-坑1).
			if err := l.git.CommitEmpty(ctx, repoDir, "piforge: initial commit"); err != nil {
				return nil, fmt.Errorf("sandbox: %w", err)
			}
		}
	}

	if b := strings.TrimSpace(branch); b != "" {
		if err := l.checkoutBranch(ctx, repoDir, b); err != nil {
			return nil, err
		}
	}

	// The agent commits inside this checkout, and a fresh host (the image has no
	// ~/.gitconfig) has no committer identity: its first `git commit` died with
	// "Author identity unknown". A repo-local identity is written here once, at
	// checkout time, so every later commit — the agent's included — works.
	if _, err := l.git.EnsureIdentity(ctx, repoDir); err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}

	sb := &contracts.Sandbox{
		ID:           id,
		UserID:       userID,
		ProjectID:    projectID,
		State:        contracts.SandboxReady,
		Provider:     ProviderName,
		Node:         l.cfg.Node,
		Image:        l.cfg.Image,
		RootDir:      sandboxDir,
		CreatedAt:    created,
		LastActiveAt: created,
	}
	if strings.TrimSpace(repoURL) != "" {
		sb.Message = "cloned " + strings.TrimSpace(repoURL)
	}
	if err := l.writeState(&stateRecord{
		ID:           sb.ID,
		UserID:       sb.UserID,
		ProjectID:    sb.ProjectID,
		State:        sb.State,
		RepoURL:      strings.TrimSpace(repoURL),
		Branch:       strings.TrimSpace(branch),
		Image:        sb.Image,
		Node:         sb.Node,
		CreatedAt:    sb.CreatedAt,
		LastActiveAt: sb.LastActiveAt,
	}); err != nil {
		return nil, err
	}
	l.adopt(sb)
	return sb, nil
}

// Get returns the sandbox with that id, from memory or from the state files.
func (l *Local) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return nil, fmt.Errorf("%w: empty sandbox id", ErrNotFound)
	}
	l.mu.Lock()
	if sb, ok := l.known[sandboxID]; ok {
		clone := *sb
		l.mu.Unlock()
		l.refresh(&clone)
		return &clone, nil
	}
	l.mu.Unlock()

	st, err := l.findState(sandboxID)
	if err != nil {
		return nil, err
	}
	sb := st.toContract()
	dir, derr := l.sandboxPath(st.UserID, st.ProjectID)
	if derr != nil {
		return nil, derr
	}
	sb.RootDir = dir
	if fi, ferr := os.Stat(dir); ferr != nil || !fi.IsDir() {
		sb.State = contracts.SandboxMissing
		sb.Message = "sandbox directory is gone"
		sb.RootDir = ""
	}
	l.adopt(sb)
	clone := *sb
	return &clone, nil
}

// Pause marks the sandbox PAUSED. The local provider keeps the directory: the
// state machine is real but nothing is snapshotted (there is nothing to
// snapshot beyond the disk).
func (l *Local) Pause(ctx context.Context, sandboxID string) error {
	sb, err := l.Get(ctx, sandboxID)
	if err != nil {
		return err
	}
	if sb.State == contracts.SandboxTerminated {
		return fmt.Errorf("%w: %s is terminated", ErrGone, sandboxID)
	}
	if sb.State == contracts.SandboxMissing {
		return fmt.Errorf("%w: %s", ErrGone, sandboxID)
	}
	return l.setState(ctx, sb, contracts.SandboxPaused, "")
}

// Resume brings a PAUSED sandbox back to READY.
func (l *Local) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	sb, err := l.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	switch sb.State {
	case contracts.SandboxTerminated:
		return nil, fmt.Errorf("%w: %s is terminated", ErrGone, sandboxID)
	case contracts.SandboxMissing:
		return nil, fmt.Errorf("%w: %s", ErrGone, sandboxID)
	}
	if err := l.setState(ctx, sb, contracts.SandboxReady, ""); err != nil {
		return nil, err
	}
	return sb, nil
}

// Destroy deletes the sandbox directory and its state file.
//
// It refuses to touch anything outside the provider root, and it resolves
// symlinks before deleting so a planted link cannot redirect the removal.
func (l *Local) Destroy(ctx context.Context, sandboxID string) error {
	sb, err := l.Get(ctx, sandboxID)
	if err != nil {
		return err
	}
	dir := sb.RootDir
	if dir == "" {
		// Already gone: destroy the state file and report success.
		_ = l.removeStateFor(sandboxID)
		l.forget(sandboxID)
		return nil
	}
	realRoot, err := evalExisting(l.cfg.Root)
	if err != nil {
		return fmt.Errorf("sandbox: resolve root: %w", err)
	}
	realDir, err := evalExisting(dir)
	if err != nil {
		return fmt.Errorf("sandbox: resolve %q: %w", dir, err)
	}
	if realDir == realRoot || !isInside(realRoot, realDir) {
		return fmt.Errorf("%w: %q", ErrDestroyRefused, dir)
	}
	if err := removeTreeWritable(realDir); err != nil {
		return fmt.Errorf("sandbox: destroy %s: %w", dir, err)
	}
	// Best effort: drop the now-empty per-user directory.
	_ = os.Remove(filepath.Dir(realDir))
	if err := l.removeStateFor(sandboxID); err != nil {
		return err
	}
	l.forget(sandboxID)
	return nil
}

// removeTreeWritable deletes a sandbox directory whose contents may be
// read-only.
//
// Mounted skills are made read-only (FR-14.2), and on Windows a read-only file
// cannot be deleted: `os.RemoveAll` would leave the sandbox half-deleted and the
// project undeletable. Clearing the read-only bits first is what keeps "delete
// this project" working after skills are mounted. Errors on individual entries
// are ignored because a missing file is success here; the final RemoveAll
// reports anything that is genuinely stuck.
func removeTreeWritable(root string) error {
	// A read-only bind mount makes its mount point busy; detach first or the
	// delete fails with EBUSY and the project is left undeletable (FR-14.2).
	unmountSkills(root)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o644)
		}
		return nil
	})
	// Directories can be read-only too (0555 blocks removing children on Unix).
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		_ = os.Chmod(p, 0o755)
		return nil
	})
	return os.RemoveAll(root)
}

// checkoutBranch switches to branch, creating it when it does not exist.
func (l *Local) checkoutBranch(ctx context.Context, dir, branch string) error {
	ok, err := l.git.BranchExists(ctx, dir, branch)
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if ok {
		if err := l.git.Checkout(ctx, dir, branch); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		return nil
	}
	if err := l.git.CreateAndCheckout(ctx, dir, branch); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	return nil
}

// cloneRepo clones into an empty directory and checks out the branch.
func (l *Local) cloneRepo(ctx context.Context, userID, dir, repoURL string) error {
	if err := l.refuseCloneTarget(ctx, userID, repoURL); err != nil {
		return err
	}
	if err := l.git.Clone(ctx, repoURL, dir, ""); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	return nil
}

// ErrRefusedCloneTarget is returned when a project's repository address names
// something this deployment will not fetch or read.
var ErrRefusedCloneTarget = errors.New("sandbox: refused clone target")

// refuseCloneTarget is the one question a tenant-supplied repository address has
// to answer before git sees it: is this something the control plane may reach?
//
// Two shapes, two rules:
//
//   - A URL (https, ssh, git://, or the scp-like `[user@]host:path`) is fetched
//     from this machine's network position, so it goes through the same outbound
//     policy as every other tenant fetch site — with the *transport's* port, not
//     a guess: an ssh clone dials 22, and checking 443 instead would have let
//     `root@127.0.0.1:acme/repo.git` through (adversarial round 5, finding 6).
//   - A local path is read, not fetched: it may be any repository this host has
//     (a development fixture), except one inside another account's sandbox. That
//     is the copy that would put somebody else's checkout behind this account's
//     files API.
//
// It is refuseTarget, the same function the push/pull/fetch guard uses, so the
// two boundaries cannot drift apart. The only difference is the sentence the
// caller reads.
func (l *Local) refuseCloneTarget(ctx context.Context, userID, repoURL string) error {
	target := strings.TrimSpace(repoURL)
	if target == "" {
		return nil
	}
	if err := refuseTarget(ctx, l.cfg.URLPolicy, l.cfg.Root, userID, "", target); err != nil {
		return fmt.Errorf("%w: %v", ErrRefusedCloneTarget, err)
	}
	return nil
}

// touch updates LastActiveAt and persists it.
func (l *Local) touch(sb *contracts.Sandbox) *contracts.Sandbox {
	sb.LastActiveAt = time.Now().UTC()
	if rec, err := l.readState(sb.UserID, sb.ProjectID); err == nil && rec != nil {
		rec.LastActiveAt = sb.LastActiveAt
		_ = l.writeState(rec)
	}
	l.adopt(sb)
	return sb
}

// refresh updates a copy with on-disk reality.
func (l *Local) refresh(sb *contracts.Sandbox) {
	if sb.RootDir == "" {
		return
	}
	if fi, err := os.Stat(sb.RootDir); err != nil || !fi.IsDir() {
		sb.State = contracts.SandboxMissing
		sb.Message = "sandbox directory is gone"
		sb.RootDir = ""
		l.adopt(sb)
	}
}

func (l *Local) setState(ctx context.Context, sb *contracts.Sandbox, state contracts.SandboxState, msg string) error {
	sb.State = state
	sb.Message = msg
	sb.LastActiveAt = time.Now().UTC()
	if err := l.writeState(&stateRecord{
		ID:           sb.ID,
		UserID:       sb.UserID,
		ProjectID:    sb.ProjectID,
		State:        state,
		Image:        sb.Image,
		Node:         sb.Node,
		CreatedAt:    sb.CreatedAt,
		LastActiveAt: sb.LastActiveAt,
		ExpiresAt:    sb.ExpiresAt,
		Message:      msg,
	}); err != nil {
		return err
	}
	l.adopt(sb)
	return nil
}

func (l *Local) adopt(sb *contracts.Sandbox) {
	if sb == nil || sb.ID == "" {
		return
	}
	l.mu.Lock()
	clone := *sb
	l.known[sb.ID] = &clone
	l.mu.Unlock()
}

func (l *Local) forget(id string) {
	l.mu.Lock()
	delete(l.known, id)
	l.mu.Unlock()
}

func (l *Local) keyMutex(id string) *sync.Mutex {
	mu := &sync.Mutex{}
	v, _ := l.keyLock.LoadOrStore(id, mu)
	return v.(*sync.Mutex)
}

// sandboxPath validates and returns <root>/<user>/<project>.
func (l *Local) sandboxPath(userID, projectID string) (string, error) {
	if err := safeSegment(userID); err != nil {
		return "", err
	}
	if err := safeSegment(projectID); err != nil {
		return "", err
	}
	p := filepath.Join(l.cfg.Root, userID, projectID)
	if !isInside(l.cfg.Root, p) {
		return "", fmt.Errorf("%w: sandbox path escapes the root", ErrInvalidID)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var unsafeSegment = []string{"/", `\`, "\x00", ":", "*", "?", "\"", "<", ">", "|"}

// safeSegment rejects identifiers that could escape a path or confuse a shell.
func safeSegment(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: empty", ErrInvalidID)
	}
	if s == "." || s == ".." || len(s) > 128 {
		return fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	for _, bad := range unsafeSegment {
		if strings.Contains(s, bad) {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidID, s, bad)
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%w: %q contains %q", ErrInvalidID, s, string(r))
		}
	}
	return nil
}

// dirIsEmpty reports whether dir has no entries.
func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // absent counts as empty: the caller creates it
		}
		return false, fmt.Errorf("sandbox: read %s: %w", dir, err)
	}
	return len(entries) == 0, nil
}

func gitxIsRepo(ctx context.Context, r *gitx.Runner, dir string) bool {
	res, err := r.Run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "true"
}

func isInside(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(p), strings.ToLower(strings.TrimSuffix(root, sep))+sep)
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

// evalExisting resolves symlinks for p, falling back to the nearest existing
// ancestor (same trick as internal/fsx).
func evalExisting(p string) (string, error) {
	cur := filepath.Clean(p)
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if cur == filepath.Clean(p) {
				return real, nil
			}
			tail, terr := filepath.Rel(cur, p)
			if terr != nil {
				return real, nil
			}
			return filepath.Join(real, tail), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		cur = parent
	}
}
