// Package provision turns "a session exists" into "a session has a working
// directory".
//
// It is the only place that knows the ordering: sandbox → git worktree → store
// row → session pointer. Keeping it separate from the HTTP layer means the
// ordering can be tested once against a real git repository instead of being
// re-derived inside three handlers.
//
// The rules it encodes come from PRD §5.1.3 and §6.4-B:
//
//   - a sandbox is keyed by (project, user) and is reused for every session in
//     that project, so a second branch costs one worktree, not one machine;
//   - a main session owns the project's default branch;
//   - a branch session owns its own worktree on its own branch;
//   - a sidecar shares its parent's worktree — that is the whole point of a
//     sidecar, and creating a second checkout would break the "same files,
//     separate context" contract.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/mcpstage"
	skillset "github.com/piforge/piforge/backend/internal/skills"
	"github.com/piforge/piforge/backend/internal/skillsnap"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
)

// Config wires the provisioner to its dependencies.
type Config struct {
	// Store is required.
	Store *store.Store
	// Sandbox is required: worktrees live inside a sandbox.
	Sandbox contracts.SandboxProvider
	// Git is required. If nil, EnsureWorktree returns an error rather than
	// silently producing a session with no files.
	Git *gitx.Runner
	// SkillsDir is where skill bundles live on the host, one directory per
	// bundle_key. Empty means skills are configured but cannot be mounted, which
	// is reported per skill rather than failing the session.
	SkillsDir string
	// SnapshotDir is where a bind mount's frozen copy of each skill is written,
	// one directory per session (FR-14.2). It must be outside the checkout in a
	// deployment: a snapshot the agent can reach through a second path would
	// defeat the read-only mount. Empty lets materialize use
	// <worktree>/.pi/skill-snapshots, which is git-excluded.
	SnapshotDir string
	// WorktreePath overrides where a worktree is placed. The default is
	// <sandbox root>/wt/<slug>, which keeps every checkout of one sandbox
	// under a single directory that is trivial to snapshot.
	WorktreePath func(sb *contracts.Sandbox, slug string) string
	// WorkspaceFiles opens the file system behind a sandbox whose workspace does
	// not exist on this disk (contracts.WorkspaceAPI, stage 2 of
	// docs/EXECUTION-BOUNDARY.md). It is how the skill projection and the
	// "does this path already exist" probe reach the sandbox: through its own API,
	// never through a volume.
	//
	// Nil means this deployment has no such sandbox — and a sandbox that needs it
	// then fails loudly instead of writing a tree the agent never sees.
	WorkspaceFiles func(ctx context.Context, sb *contracts.Sandbox) (materialize.FileSystem, error)
	// LimitsFor resolves a tenant's quota (internal/tenantlimits). It is where the
	// concurrency guard learns how many sandboxes this tenant may have; a nil
	// resolver means "no per-tenant policy", which keeps the single-tenant
	// behaviour of a deployment that configured none.
	//
	// The container limits themselves are applied by the sandbox provider, which
	// receives the same policy; this one is only about refusing to create the
	// sandbox at all.
	LimitsFor func(ctx context.Context, userID string) tenantlimits.Limits
}

// Service provisions working directories.
type Service struct {
	cfg Config
}

// New builds a provisioner. It panics on a missing Store or Sandbox because
// those are wiring mistakes, not runtime conditions.
func New(cfg Config) *Service {
	if cfg.Store == nil {
		panic("provision: Store is required")
	}
	if cfg.Sandbox == nil {
		panic("provision: Sandbox is required")
	}
	if cfg.WorktreePath == nil {
		cfg.WorktreePath = defaultWorktreePath
	}
	return &Service{cfg: cfg}
}

func defaultWorktreePath(sb *contracts.Sandbox, slug string) string {
	return joinWorkspace(RepoDir(sb), "..", "worktrees", slug)
}

// RepoDir is the primary checkout of a sandbox. The local provider lays a
// sandbox out as <root>/repo plus <root>/worktrees/<name>; nesting worktrees
// inside the checkout would show up as untracked noise in the main status, so
// they live beside it instead.
//
// With a detached workspace RootDir is the container path ("/workspace"), and so
// is everything derived from it: those paths are what the store keeps and what
// the panels and the sandbox API address, because the control plane has no other
// name for them.
func RepoDir(sb *contracts.Sandbox) string {
	return joinWorkspace(sb.RootDir, "repo")
}

// joinWorkspace joins path elements under a sandbox root in the syntax that root
// uses: a container root is POSIX whatever this process runs on, and
// filepath.Join would turn "/workspace/repo" into "\workspace\repo" on a Windows
// control plane — a path the sandbox has never heard of.
func joinWorkspace(root string, parts ...string) string {
	if strings.HasPrefix(root, "/") {
		return path.Join(append([]string{root}, parts...)...)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// cleanWorkspacePath normalises a path in the syntax it is written in, so a
// stored row and a freshly derived path compare equal on every platform.
func cleanWorkspacePath(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return filepath.Clean(p)
}

// EnsureWorktree makes sure the session has a usable worktree and returns it.
//
// It is idempotent: calling it twice returns the same row and does not create a
// second checkout. That matters because the HTTP layer calls it from both
// "create session" and "first message", and either can win the race.
func (s *Service) EnsureWorktree(
	ctx context.Context,
	userID string,
	project *contracts.Project,
	sess *contracts.Session,
) (*contracts.Worktree, error) {
	if project == nil || sess == nil {
		return nil, fmt.Errorf("provision: project and session are required")
	}
	if project.UserID != userID || sess.UserID != userID {
		return nil, fmt.Errorf("provision: tenant mismatch")
	}

	// Already attached: the checkout is the same, but the projection is
	// re-run. It is idempotent (an existing mounted skill with the same row is
	// kept, edits still do not reach a running session), and it repairs the one
	// state a restart can leave behind: a checkout whose `.pi/skills` was a bind
	// mount target before the container restarted is now an empty directory, so
	// without this pass the agent would have no skills at all until the session
	// was recreated. A failure is logged rather than fatal — the session and its
	// files are usable, and the next pass tries again.
	if sess.WorktreeID != "" {
		wt, err := s.cfg.Store.Worktree(ctx, userID, sess.WorktreeID)
		if err == nil {
			// The projection needs to know which sandbox holds the checkout: a
			// detached workspace is written through the sandbox API, and a
			// missing row would otherwise silently become a host write into a
			// path that does not exist.
			sb, serr := s.cfg.Store.Sandbox(ctx, userID, wt.SandboxID)
			if serr != nil {
				slog.Warn("tooling refresh skipped: the worktree's sandbox could not be loaded",
					"session", sess.ID, "sandbox", wt.SandboxID, "error", serr)
				return wt, nil
			}
			if merr := s.mountTooling(ctx, userID, sess.ID, sb, wt.Path); merr != nil {
				slog.Warn("tooling refresh on an existing worktree failed",
					"session", sess.ID, "error", merr)
			}
			return wt, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("provision: load worktree: %w", err)
		}
		// The row vanished; fall through and rebuild it.
	}

	// A sidecar attaches to its parent's checkout. If the parent has none yet,
	// provision it first — a sidecar must never get its own directory.
	if sess.Kind == contracts.KindSidecar {
		if sess.ParentID == "" {
			return nil, fmt.Errorf("provision: sidecar %s has no parent", sess.ID)
		}
		parent, err := s.cfg.Store.Session(ctx, userID, sess.ParentID)
		if err != nil {
			return nil, fmt.Errorf("provision: load sidecar parent: %w", err)
		}
		parentWT, err := s.EnsureWorktree(ctx, userID, project, parent)
		if err != nil {
			return nil, err
		}
		if err := s.attach(ctx, userID, sess.ID, parentWT.ID); err != nil {
			return nil, err
		}
		// The sidecar shares the parent's checkout, so the skill versions it can
		// see are the parent's. Copying the records keeps its own report a fact
		// instead of a join that changes when the parent is re-provisioned.
		if _, err := s.cfg.Store.CopySessionSkills(ctx, userID, parent.ID, sess.ID); err != nil {
			return nil, fmt.Errorf("provision: copy parent skill records: %w", err)
		}
		return parentWT, nil
	}

	sb, err := s.ensureSandbox(ctx, userID, project)
	if err != nil {
		return nil, err
	}
	if s.cfg.Git == nil {
		return nil, fmt.Errorf("provision: git runner is not configured")
	}

	branch := BranchFor(project, sess)

	// The main session works directly in the sandbox's primary checkout, which
	// is already on the default branch. `git worktree add main` cannot work:
	// that branch is checked out there. Branches get a separate worktree so the
	// main checkout stays on the branch the user thinks they are on.
	if sess.Kind != contracts.KindBranch {
		repoDir := RepoDir(sb)
		if existing, err := s.findWorktreeByPath(ctx, userID, sb.ID, repoDir); err != nil {
			return nil, err
		} else if existing != nil {
			if err := s.attach(ctx, userID, sess.ID, existing.ID); err != nil {
				return nil, err
			}
			if err := s.mountTooling(ctx, userID, sess.ID, sb, existing.Path); err != nil {
				return nil, err
			}
			return existing, nil
		}

		base := ""
		if head, err := s.headCommit(ctx, sb, repoDir); err == nil {
			base = head
		}
		wt, err := s.cfg.Store.CreateWorktree(ctx, userID, store.NewWorktree{
			SandboxID:  sb.ID,
			Path:       repoDir,
			Branch:     branch,
			BaseCommit: base,
		})
		if err != nil {
			return nil, fmt.Errorf("provision: record primary worktree: %w", err)
		}
		if err := s.attach(ctx, userID, sess.ID, wt.ID); err != nil {
			return nil, err
		}
		if err := s.mountTooling(ctx, userID, sess.ID, sb, wt.Path); err != nil {
			return nil, err
		}
		return wt, nil
	}

	// The slug is the branch name without its namespace. Slug() must not run
	// again here: it truncates at 48 characters, which would cut off the
	// session-id suffix that makes the name unique and put two sessions back on
	// one path.
	slug := branchSlug(branch)
	path := s.cfg.WorktreePath(sb, slug)

	// Reuse an existing row for this path before touching git, so a retry does
	// not trip over "already a worktree".
	if existing, err := s.findWorktreeByPath(ctx, userID, sb.ID, path); err != nil {
		return nil, err
	} else if existing != nil {
		// Only *this* session's own row is a retry. A row at this path owned by
		// another session is a collision, and silently attaching to it is the
		// bug that made a second clone of one session share the first clone's
		// checkout: both derived `piforge/<label>` from the same title, so both
		// got one worktree and one set of files (FR-2.7).
		current, cerr := s.cfg.Store.Session(ctx, userID, sess.ID)
		if cerr != nil {
			return nil, fmt.Errorf("provision: reload session: %w", cerr)
		}
		if current.WorktreeID != existing.ID {
			return nil, fmt.Errorf(
				"provision: worktree path %q already belongs to another session (branch %q); refusing to share a checkout",
				existing.Path, existing.Branch)
		}
		if err := s.attach(ctx, userID, sess.ID, existing.ID); err != nil {
			return nil, err
		}
		// A branch session has its own checkout, so the mount has to happen here
		// too: sharing the projection with the main checkout would leave the
		// agent with no skills at all.
		if err := s.mountTooling(ctx, userID, sess.ID, sb, existing.Path); err != nil {
			return nil, err
		}
		return existing, nil
	}

	// A directory with no row is not a worktree this service made: reusing it
	// would put two sessions in whatever files happen to be there. Report it
	// rather than letting `git worktree add` fail with its own phrasing. For a
	// detached workspace the question is asked through the sandbox API, because
	// that is the only place the directory exists.
	if exists, err := s.pathExists(ctx, sb, path); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("provision: worktree path %q already exists; refusing to reuse it", path)
	}

	if err := s.addWorktree(ctx, sb, RepoDir(sb), path, branch); err != nil {
		return nil, fmt.Errorf("provision: add worktree %s at %s: %w", branch, path, err)
	}

	base := ""
	if head, err := s.headCommit(ctx, sb, RepoDir(sb)); err == nil {
		base = head
	}

	wt, err := s.cfg.Store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID:  sb.ID,
		Path:       path,
		Branch:     branch,
		BaseCommit: base,
	})
	if err != nil {
		return nil, fmt.Errorf("provision: record worktree: %w", err)
	}
	if err := s.attach(ctx, userID, sess.ID, wt.ID); err != nil {
		return nil, err
	}
	if err := s.mountTooling(ctx, userID, sess.ID, sb, wt.Path); err != nil {
		return nil, err
	}
	return wt, nil
}

// addWorktree creates a branch session's linked worktree, inside the sandbox
// when the provider can run commands there and on the control plane's own
// filesystem otherwise.
//
// The choice follows the provider, not a flag: a linked worktree's
// administrative files (`<main>/.git/worktrees/<slug>` and the `.git` file in the
// worktree) belong to whoever ran `git worktree add`. With -sandbox docker the
// panel's shell and git run inside the container as `gem`, so a worktree created
// by the host leaves those files owned by another account and points the `.git`
// file at a host path the container cannot resolve: the first `git stage` in a
// branch session fails with
//
//	fatal: Unable to create '.../.git/worktrees/<slug>/index.lock': Permission denied
//
// A provider that cannot enter its sandbox has no such second account — the local
// provider's directories are the same filesystem the control plane's git runs on
// — and keeps using gitx (which also validates the branch and path on the host).
// Existing worktrees are never rewritten: the reuse checks above return the
// recorded row before this is reached.
func (s *Service) addWorktree(ctx context.Context, sb *contracts.Sandbox, repoDir, path, branch string) error {
	if inside, ok := s.cfg.Sandbox.(contracts.WorktreeSandbox); ok {
		return inside.AddWorktree(ctx, sb.ID, repoDir, path, branch)
	}
	return s.cfg.Git.AddWorktree(ctx, repoDir, path, branch, true)
}

// mountTooling projects the user's skills and MCP servers into a checkout and
// records which skill versions the session was given (FR-14.5).
//
// It runs on every provisioning pass rather than only the first, because the
// configuration can change while a session lives: adding an MCP server should
// reach existing sessions the next time they are touched, and the mount is
// idempotent by construction. The *record*, however, is written once: a later
// pass cannot rewrite what this session was mounted with.
func (s *Service) mountTooling(ctx context.Context, userID, sessionID string, sb *contracts.Sandbox, root string) error {
	if sb == nil {
		return fmt.Errorf("provision: mount tooling: the sandbox is required")
	}
	remote := sb.Workspace == contracts.WorkspaceAPI
	var fs materialize.FileSystem
	if remote {
		// The checkout is only inside the sandbox: the projection is a request to
		// the sandbox's own API, made as the account that owns the tree.
		opened, err := s.workspaceFiles(ctx, sb)
		if err != nil {
			return fmt.Errorf("provision: mount tooling: %w", err)
		}
		fs = opened
	} else if info, err := os.Stat(root); err != nil || !info.IsDir() {
		// A provider that creates the worktree itself (the docker sandbox) already
		// has it on this path; a provider that has not must not be second-guessed by
		// creating a *host* directory that the sandbox will never see. The old bug
		// this guards against is the host's git touching a container-owned checkout.
		slog.Warn("tooling not mounted: the checkout is not on this filesystem", "root", root)
		return nil
	}
	all, err := s.cfg.Store.ListSkills(ctx, userID)
	if err != nil {
		return fmt.Errorf("provision: list skills: %w", err)
	}
	// Resolve the two scopes *before* mounting. A private skill shadows an
	// enabled shared skill with the same name (FR-14.1); a disabled private row
	// does not, so the shared one takes over again. Passing both rows to the
	// materializer would leave the winner to map/iteration order, which is how
	// "same name" used to be an accident rather than a rule.
	//
	// The same list is what the session record is built from, so `session_skills`
	// names the row that was actually mounted — not whichever row happened to be
	// last in a SQL result (FR-14.5).
	skills := skillset.Effective(all)
	servers, err := s.cfg.Store.ListMCPServers(ctx, userID)
	if err != nil {
		return fmt.Errorf("provision: list mcp servers: %w", err)
	}
	if len(skills) == 0 && len(servers) == 0 {
		// Still mount: an empty configuration must clear anything a previous
		// pass wrote, or a removed MCP server keeps running.
	}
	// An empty SnapshotDir keeps materialize's own fallback
	// (<worktree>/.pi/skill-snapshots), which is what a test or a hand-built
	// configuration without a data directory needs.
	snapshotDir := ""
	if s.cfg.SnapshotDir != "" {
		snapshotDir = skillsnap.Dir(s.cfg.SnapshotDir, sessionID)
	}
	options := materialize.Options{
		SkillsDir: s.cfg.SkillsDir,
		// One snapshot directory per session: the mounted tree is frozen, so a
		// new session gets a new snapshot while a running one keeps its own.
		// Sharing the snapshot with the bundle (or with another live session)
		// would let an edit reach a session that already started. The directory
		// is named after the session id so the delete path (and the janitor's
		// sweep) can map it back to a session without a second index.
		SnapshotDir: snapshotDir,
		// A detached workspace is an isolated agent (stage 4: every docker and
		// runner deployment), so the servers this placement cannot stage are
		// recorded in the manifest instead of vanishing into a log line.
		MCPUnavailable: mcpstage.Omissions(servers, remote),
	}
	for name, omission := range options.MCPUnavailable {
		slog.Warn("an MCP server will not reach this session's agent",
			"session", sessionID, "server", name, "code", omission.Code, "reason", omission.Reason)
	}
	var result materialize.Result
	if remote {
		result, err = materialize.MountFS(fs, root, skills, servers, options)
	} else {
		result, err = materialize.Mount(root, skills, servers, options)
	}
	if err != nil {
		return fmt.Errorf("provision: mount tooling: %w", err)
	}
	for name, reason := range result.Skipped {
		slog.Warn("skill not mounted", "skill", name, "reason", reason)
	}
	for _, warning := range result.Warnings {
		// A degraded mechanism is not a failure, but it is not silent either:
		// the operator asked for a mount and this host could only copy.
		slog.Warn("skill mount degraded", "session", sessionID, "detail", warning)
	}

	if sessionID != "" && len(result.Mounted) > 0 {
		byName := make(map[string]*contracts.Skill, len(skills))
		for _, skill := range skills {
			if skill != nil {
				byName[skill.Name] = skill
			}
		}
		records := make([]store.NewSessionSkill, 0, len(result.Mounted))
		for _, mounted := range result.Mounted {
			skill := byName[mounted.Name]
			if skill == nil {
				continue
			}
			// The digest is taken from the mounted copy, not from the source
			// bundle: what the agent reads is the fact worth recording. For a
			// detached workspace that copy is read back through the sandbox API,
			// which costs a request per file and doubles as the proof that the
			// projection landed.
			var digest string
			var derr error
			if remote {
				digest, derr = materialize.DigestFS(fs, mounted.Dir)
			} else {
				digest, derr = materialize.Digest(mounted.Dir)
			}
			if derr != nil {
				slog.Warn("mounted skill could not be fingerprinted",
					"skill", mounted.Name, "error", derr)
				continue
			}
			version := skill.Version
			if version == "" {
				version = "0.0.0"
			}
			records = append(records, store.NewSessionSkill{
				SkillID:     skill.ID,
				Name:        skill.Name,
				Version:     version,
				ContentHash: digest,
				Description: skill.Description,
				// Recorded with forward slashes: the API and the UI show this path,
				// and a Windows host path with backslashes would render as one
				// unreadable run once it reaches JSON (the backslash is an escape).
				MountedPath: filepath.ToSlash(mounted.Dir),
			})
		}
		if _, err := s.cfg.Store.RecordSessionSkills(ctx, userID, sessionID, records); err != nil {
			return fmt.Errorf("provision: record mounted skills: %w", err)
		}
	}
	return nil
}

// workspaceFiles opens the file system behind a sandbox that exists only inside
// its container. A deployment without that seam reports it instead of writing the
// projection into a path nothing will read.
func (s *Service) workspaceFiles(ctx context.Context, sb *contracts.Sandbox) (materialize.FileSystem, error) {
	if s.cfg.WorkspaceFiles == nil {
		return nil, fmt.Errorf("sandbox %s has no workspace on this filesystem (Workspace=%s) and no sandbox file API is configured",
			sb.ID, sb.Workspace.Normalized())
	}
	fs, err := s.cfg.WorkspaceFiles(ctx, sb)
	if err != nil {
		return nil, fmt.Errorf("open the workspace of sandbox %s through its API: %w", sb.ID, err)
	}
	return fs, nil
}

// pathExists reports whether a path already exists in the sandbox's workspace.
//
// It is the guard that stops a branch session from adopting a directory no row
// owns. On this disk it is a stat; for a detached workspace it is a question for
// the sandbox, because the directory is not here to stat.
func (s *Service) pathExists(ctx context.Context, sb *contracts.Sandbox, p string) (bool, error) {
	if sb.Workspace != contracts.WorkspaceAPI {
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("provision: inspect worktree path %q: %w", p, err)
		}
		return true, nil
	}
	fs, err := s.workspaceFiles(ctx, sb)
	if err != nil {
		return false, err
	}
	kind, err := fs.Lookup(p)
	if err != nil {
		return false, fmt.Errorf("provision: inspect worktree path %q in sandbox %s: %w", p, sb.ID, err)
	}
	return kind != materialize.KindMissing, nil
}

// headCommit reads the checkout's HEAD as the base commit a worktree record
// keeps. It is informational, so a failure is reported to the caller (which
// records an empty base) rather than failing the provisioning pass.
//
// Only a workspace on this disk can be read with this process's git. A detached
// workspace would need the same command through the sandbox, and the worktree
// creation that just ran there already proved the repository exists; the record's
// base_commit stays empty for it (docs/EXECUTION-BOUNDARY.md, remaining work).
func (s *Service) headCommit(ctx context.Context, sb *contracts.Sandbox, repoDir string) (string, error) {
	if sb.Workspace == contracts.WorkspaceAPI {
		return "", fmt.Errorf("provision: the checkout of sandbox %s is only inside the container", sb.ID)
	}
	return s.cfg.Git.Head(ctx, repoDir)
}

// ensureSandbox returns the project's sandbox, creating it when absent.
func (s *Service) ensureSandbox(
	ctx context.Context,
	userID string,
	project *contracts.Project,
) (*contracts.Sandbox, error) {
	if sb, err := s.cfg.Store.SandboxForProject(ctx, userID, project.ID); err == nil {
		// FR-5.2: opening a session wakes its sandbox. A paused sandbox is resumed here
		// rather than reported as an error, because the user's intent — "show me this
		// session" — is unambiguous, and making them find a wake button first is a
		// round trip that teaches nothing.
		if sb.State == contracts.SandboxPaused {
			if live, rerr := s.cfg.Sandbox.Resume(ctx, sb.ID); rerr == nil {
				if uerr := s.cfg.Store.UpdateSandboxState(ctx, userID, sb.ID, live.State, ""); uerr == nil {
					sb.State = live.State
				}
			}
		}
		return sb, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("provision: load sandbox: %w", err)
	}

	// The concurrency guard, before anything is created: a tenant may hold at most
	// N sandboxes at once, and N is the policy's, not this function's. It is
	// checked here because this is the only place a sandbox row is ever created —
	// a second call site would be a second place to forget the check.
	if err := s.checkSandboxQuota(ctx, userID); err != nil {
		return nil, err
	}

	live, err := s.cfg.Sandbox.Ensure(ctx, userID, project.ID, project.RepoURL, project.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("provision: create sandbox: %w", err)
	}

	sb, err := s.cfg.Store.CreateSandbox(ctx, userID, store.NewSandbox{
		// The provider's id is the handle every later lifecycle call (pause, resume,
		// destroy, endpoint, exec) takes. Keeping the store's own id here made the
		// docker provider look for a container named after a uuid it had never seen.
		ID:        live.ID,
		ProjectID: project.ID,
		State:     live.State,
		Provider:  live.Provider,
		Node:      live.Node,
		Image:     live.Image,
		RootDir:   live.RootDir,
		// Where the workspace is, as this process can reach it. It decides every
		// later write: a detached sandbox (stage 2) has no directory here, and the
		// projection has to go through the sandbox's API instead.
		Workspace: live.Workspace,
		ExpiresAt: live.ExpiresAt,
		// One key per sandbox, generated where the container was created (or the
		// fixed -sandbox-api-key override). It is sealed by the store and used for
		// every later call to this sandbox's API.
		APIKey: live.SandboxAPIKey,
		// The limits the provider actually applied. They are recorded on the row so
		// an operator can see the effective quota of this sandbox later, even after
		// the policy file changed (GET /v1/admin/status reports them).
		CPUMilli: live.CPUMilli,
		MemMB:    live.MemMB,
	})
	if err != nil {
		return nil, fmt.Errorf("provision: record sandbox: %w", err)
	}
	return sb, nil
}

// checkSandboxQuota refuses a new sandbox when the tenant already holds as many
// as its policy allows.
//
// The error is typed (contracts.ErrTenantSandboxLimit) and carries the numbers:
// the client shows "you have 3 of 3 sandboxes; delete or pause a project", which
// is the only thing a user can act on. Zero in the policy means unlimited, and a
// deployment with no policy at all keeps unlimited — the guard is a configured
// ceiling, not a default assumption about how many projects a person may own.
func (s *Service) checkSandboxQuota(ctx context.Context, userID string) error {
	if s.cfg.LimitsFor == nil {
		return nil
	}
	limits := s.cfg.LimitsFor(ctx, userID)
	if limits.MaxSandboxes <= 0 {
		return nil
	}
	count, err := s.cfg.Store.SandboxCountForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("provision: count sandboxes: %w", err)
	}
	if count < limits.MaxSandboxes {
		return nil
	}
	message := fmt.Sprintf("this tenant already has %d sandboxes, which is the limit (%d): delete or pause a project's sandbox before creating another",
		count, limits.MaxSandboxes)
	slog.WarnContext(ctx, "sandbox creation refused by the tenant quota",
		slog.String("user", userID), slog.Int("sandboxes", count), slog.Int("limit", limits.MaxSandboxes))
	return &contracts.APIError{
		Code:    contracts.ErrTenantSandboxLimit,
		Message: message,
		Details: map[string]any{
			"limit":   limits.MaxSandboxes,
			"current": count,
			"tenant":  userID,
		},
	}
}

func (s *Service) findWorktreeByPath(
	ctx context.Context,
	userID, sandboxID, path string,
) (*contracts.Worktree, error) {
	list, err := s.cfg.Store.ListWorktrees(ctx, userID, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("provision: list worktrees: %w", err)
	}
	for _, wt := range list {
		if cleanWorkspacePath(wt.Path) == cleanWorkspacePath(path) {
			return wt, nil
		}
	}
	return nil, nil
}

func (s *Service) attach(ctx context.Context, userID, sessionID, worktreeID string) error {
	if _, err := s.cfg.Store.UpdateSession(ctx, userID, sessionID,
		store.SessionPatch{WorktreeID: &worktreeID}); err != nil {
		return fmt.Errorf("provision: attach worktree to session: %w", err)
	}
	return nil
}

// BranchFor decides which git branch a session works on.
//
// Main sessions use the project's default branch so their commits land where a
// user expects. Branches get a namespaced name so they can never collide with
// a human branch and are recognisable in `git branch` output.
//
// A branch name must be unique per *session*, not per title: two clones of one
// session both carry the title "‹label› 的副本" and used to derive the same
// `piforge/<label>`, so the second clone reused the first clone's worktree and
// its files (FR-2.7). The session id is unique, so a short tail of it is mixed
// into the name. The result is stable for one session — provisioning twice
// yields the same branch — which is what makes EnsureWorktree idempotent.
func BranchFor(project *contracts.Project, sess *contracts.Session) string {
	if sess.Kind != contracts.KindBranch {
		if project.DefaultBranch != "" {
			return project.DefaultBranch
		}
		return "main"
	}
	tail := sessionSuffix(sess.ID)
	base := Slug(sess.Label)
	if strings.TrimSpace(sess.Label) == "" || base == "session" {
		// Nothing usable in the label (empty, punctuation-only, or a title made
		// entirely of characters the slug drops). The id alone still says which
		// attempt this is.
		return "piforge/" + tail
	}
	return "piforge/" + base + "-" + tail
}

// branchSlug is the part of a branch name that becomes a directory name.
func branchSlug(branch string) string {
	if rest, ok := strings.CutPrefix(branch, "piforge/"); ok {
		return rest
	}
	return Slug(branch)
}

// sessionSuffix is the last few characters of a session id, restricted to
// characters that are safe in a ref and a directory name.
//
// Session ids are unique; their tails are random, so the suffix distinguishes
// two clones that share a title without lengthening the name with the whole id.
func sessionSuffix(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(id)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 8 {
		s = s[len(s)-8:]
	}
	if s == "" {
		return "session"
	}
	return s
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns arbitrary text into a filesystem- and ref-safe fragment.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "piforge/", "")
	s = slugUnsafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "session"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}
