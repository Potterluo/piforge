// Package workspaceremote implements contracts.Workspace against the file
// service of a sandbox container.
//
// Why this exists: with the docker provider the agent, the browser and the
// terminal all run inside one container and see /workspace, while the panel
// used to read the bind-mounted host directory. Those are the same bytes on a
// single-machine deployment and two different views everywhere else, and even
// on one machine a path can exist on the host and be invisible to the agent (or
// the other way round: the agent writes something the host never sees, because
// the daemon is not local). The panel and the agent must ask the same machine
// the same question, so this workspace asks the container.
//
// Git: the container has git, and internal/gitx already owns the porcelain
// parsing, the path validation and the "nothing to commit" classification. The
// cheap correct route is therefore to keep gitx and give it an executor that
// runs the very same argv inside the sandbox (see ShellExecutor) rather than to
// reimplement a second, subtly different git reader. Every git method below is
// gitx's, unchanged; only the machine executing it differs.
//
// Deliberate differences from internal/fsx:
//
//   - symlinks are not resolved and re-checked remotely. The containment that
//     matters here is the container boundary: the agent whose files these are
//     could read /etc/passwd inside its own sandbox anyway, so a link cannot
//     widen anything. Lexical traversal of the workspace root is still rejected
//     by fsx.CleanRel before a path is joined.
//   - a file larger than the preview threshold is read as head+tail through
//     `head`/`tail` instead of ReadAt. The cut can land inside a multi-byte
//     character; the local reader trims to a line boundary, this one reports
//     Truncated and lets the UI offer the download.
package workspaceremote

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// Target is one workspace root as the sandbox sees it.
type Target struct {
	// Client speaks to the sandbox's API.
	Client *aio.Client
	// Root is the directory inside the container, e.g. "/workspace/repo".
	Root string
}

// Backend answers "which sandbox owns this host path, and where is it there".
//
// It is the hook cmd/piforged fills in: the store turns the worktree path a
// session resolved into a sandbox id, the docker provider turns that into an
// endpoint, and the client is cached per sandbox.
type Backend interface {
	Target(ctx context.Context, hostPath string) (Target, error)
}

// Config configures a Workspace.
type Config struct {
	// Backend is required.
	Backend Backend
	// Git is the runner whose commands execute inside the sandbox. It must have
	// been built with WithExecutor(ShellExecutor{...}); when nil, git methods
	// answer fsx.ErrNoGitBackend and the tree carries no status letters.
	Git *gitx.Runner
	// ShowIgnored reveals dot directories and node_modules to every caller.
	ShowIgnored bool
	// MaxEntriesPerDir caps one directory listing.
	MaxEntriesPerDir int
	// MaxInlineBytes is the whole-body limit, MaxPreviewBytes the metadata-only
	// limit (PRD FR-7.8).
	MaxInlineBytes  int64
	MaxPreviewBytes int64
	// HeadBytes / TailBytes bound the head+tail preview of a large file.
	HeadBytes int64
	TailBytes int64
	// SearchLimit caps hits per search.
	SearchLimit int
	// Audit is called after every successful write/delete, like fsx.Config.Audit.
	Audit fsx.AuditFunc
}

func (c Config) normalise() Config {
	if c.MaxEntriesPerDir <= 0 {
		c.MaxEntriesPerDir = fsx.DefaultMaxEntries
	}
	if c.MaxInlineBytes <= 0 {
		c.MaxInlineBytes = fsx.DefaultMaxInline
	}
	if c.MaxPreviewBytes <= c.MaxInlineBytes {
		c.MaxPreviewBytes = fsx.DefaultMaxPreview
	}
	if c.HeadBytes <= 0 {
		c.HeadBytes = fsx.DefaultHeadBytes
	}
	if c.TailBytes <= 0 {
		c.TailBytes = fsx.DefaultTailBytes
	}
	if c.SearchLimit <= 0 {
		c.SearchLimit = fsx.DefaultSearchLimit
	}
	return c
}

// Workspace implements contracts.Workspace over one sandbox per call. It holds
// no mutable state and is safe for concurrent use.
type Workspace struct {
	cfg Config
}

var _ contracts.Workspace = (*Workspace)(nil)

// New builds a Workspace.
func New(cfg Config) (*Workspace, error) {
	if cfg.Backend == nil {
		return nil, fmt.Errorf("workspaceremote: a backend is required")
	}
	return &Workspace{cfg: cfg.normalise()}, nil
}

// Config returns the effective configuration.
func (w *Workspace) Config() Config { return w.cfg }

// target resolves a host root. Every method goes through it, so a failure to
// find the sandbox is reported instead of silently reading the host.
func (w *Workspace) target(ctx context.Context, root string) (Target, error) {
	if strings.TrimSpace(root) == "" {
		return Target{}, fmt.Errorf("workspaceremote: empty workspace root")
	}
	tgt, err := w.cfg.Backend.Target(ctx, root)
	if err != nil {
		return Target{}, fmt.Errorf("workspaceremote: resolve %q: %w", root, err)
	}
	if tgt.Client == nil || strings.TrimSpace(tgt.Root) == "" {
		return Target{}, fmt.Errorf("workspaceremote: the backend resolved %q to no sandbox path", root)
	}
	return tgt, nil
}

func (w *Workspace) audit(ev fsx.AuditEvent) {
	if w.cfg.Audit != nil {
		w.cfg.Audit(ev)
	}
}

// ---------------------------------------------------------------------------
// Git (delegated to the container-backed runner)
// ---------------------------------------------------------------------------

func (w *Workspace) git() (*gitx.Runner, error) {
	if w.cfg.Git == nil {
		return nil, fsx.ErrNoGitBackend
	}
	return w.cfg.Git, nil
}

// Status delegates to the container's git.
func (w *Workspace) Status(ctx context.Context, root string) (*contracts.GitStatus, error) {
	g, err := w.git()
	if err != nil {
		return nil, err
	}
	return g.Status(ctx, root)
}

// Diff delegates to the container's git.
func (w *Workspace) Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error) {
	g, err := w.git()
	if err != nil {
		return nil, err
	}
	return g.Diff(ctx, root, req)
}

// Log delegates to the container's git.
func (w *Workspace) Log(ctx context.Context, root string, limit int, cursor string) (*contracts.GitGraph, error) {
	g, err := w.git()
	if err != nil {
		return nil, err
	}
	return g.Log(ctx, root, limit, cursor)
}

// Stage delegates to the container's git.
func (w *Workspace) Stage(ctx context.Context, root string, paths []string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.Stage(ctx, root, paths)
}

// Unstage delegates to the container's git.
func (w *Workspace) Unstage(ctx context.Context, root string, paths []string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.Unstage(ctx, root, paths)
}

// Commit delegates to the container's git.
func (w *Workspace) Commit(ctx context.Context, root string, req contracts.CommitRequest) (string, error) {
	g, err := w.git()
	if err != nil {
		return "", err
	}
	return g.Commit(ctx, root, req)
}

// CreateBranch delegates to the container's git.
func (w *Workspace) CreateBranch(ctx context.Context, root, name string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.CreateBranch(ctx, root, name)
}

// Checkout delegates to the container's git.
func (w *Workspace) Checkout(ctx context.Context, root, name string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.Checkout(ctx, root, name)
}

// Adopt carries work between two checkouts. Both roots are resolved to their
// own sandbox; in practice they are two worktrees of one project, i.e. one
// container.
func (w *Workspace) Adopt(ctx context.Context, sourceRoot, targetRoot, message string) (contracts.AdoptResult, error) {
	g, err := w.git()
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	return g.Adopt(ctx, sourceRoot, targetRoot, message)
}

// StageHunk delegates to the container's git.
func (w *Workspace) StageHunk(ctx context.Context, root, path string, index int) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.StageHunk(ctx, root, path, index)
}

// UnstageHunk delegates to the container's git.
func (w *Workspace) UnstageHunk(ctx context.Context, root, path string, index int) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.UnstageHunk(ctx, root, path, index)
}

// DiscardHunk delegates to the container's git.
func (w *Workspace) DiscardHunk(ctx context.Context, root, path string, index int) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.DiscardHunk(ctx, root, path, index)
}

// DiscardFile delegates to the container's git.
func (w *Workspace) DiscardFile(ctx context.Context, root, path string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.DiscardFile(ctx, root, path)
}

// Push delegates to the container's git.
func (w *Workspace) Push(ctx context.Context, root string) (contracts.RemoteResult, error) {
	g, err := w.git()
	if err != nil {
		return contracts.RemoteResult{}, err
	}
	return g.Push(ctx, root)
}

// Pull delegates to the container's git. gitx refuses a merge (fast-forward
// only) exactly as it does on the host.
func (w *Workspace) Pull(ctx context.Context, root string) (contracts.RemoteResult, error) {
	g, err := w.git()
	if err != nil {
		return contracts.RemoteResult{}, err
	}
	return g.Pull(ctx, root)
}

// Fetch delegates to the container's git.
func (w *Workspace) Fetch(ctx context.Context, root string) error {
	g, err := w.git()
	if err != nil {
		return err
	}
	return g.Fetch(ctx, root)
}

// AheadBehind delegates to the container's git.
func (w *Workspace) AheadBehind(ctx context.Context, root string) (int, int, error) {
	g, err := w.git()
	if err != nil {
		return 0, 0, err
	}
	return g.AheadBehind(ctx, root)
}

// PRRange delegates to the container's git: the preview has to describe the
// same checkout the push will send.
func (w *Workspace) PRRange(ctx context.Context, root, base string) (contracts.PRRange, error) {
	g, err := w.git()
	if err != nil {
		return contracts.PRRange{}, err
	}
	return g.PRRange(ctx, root, base)
}
