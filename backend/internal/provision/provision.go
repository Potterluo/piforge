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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/store"
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
	// WorktreePath overrides where a worktree is placed. The default is
	// <sandbox root>/wt/<slug>, which keeps every checkout of one sandbox
	// under a single directory that is trivial to snapshot.
	WorktreePath func(sb *contracts.Sandbox, slug string) string
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
	return filepath.Join(RepoDir(sb), "..", "worktrees", slug)
}

// RepoDir is the primary checkout of a sandbox. The local provider lays a
// sandbox out as <root>/repo plus <root>/worktrees/<name>; nesting worktrees
// inside the checkout would show up as untracked noise in the main status, so
// they live beside it instead.
func RepoDir(sb *contracts.Sandbox) string {
	return filepath.Join(sb.RootDir, "repo")
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

	// Already attached: nothing to do.
	if sess.WorktreeID != "" {
		wt, err := s.cfg.Store.Worktree(ctx, userID, sess.WorktreeID)
		if err == nil {
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
			if err := s.mountTooling(ctx, userID, sess.ID, existing.Path); err != nil {
				return nil, err
			}
			return existing, nil
		}

		base := ""
		if head, err := s.cfg.Git.Head(ctx, repoDir); err == nil {
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
		if err := s.mountTooling(ctx, userID, sess.ID, wt.Path); err != nil {
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
		if err := s.mountTooling(ctx, userID, sess.ID, existing.Path); err != nil {
			return nil, err
		}
		return existing, nil
	}

	// A directory with no row is not a worktree this service made: reusing it
	// would put two sessions in whatever files happen to be there. Report it
	// rather than letting `git worktree add` fail with its own phrasing.
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("provision: worktree path %q already exists; refusing to reuse it", path)
	}

	if err := s.addWorktree(ctx, sb, RepoDir(sb), path, branch); err != nil {
		return nil, fmt.Errorf("provision: add worktree %s at %s: %w", branch, path, err)
	}

	base := ""
	if head, err := s.cfg.Git.Head(ctx, RepoDir(sb)); err == nil {
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
	if err := s.mountTooling(ctx, userID, sess.ID, wt.Path); err != nil {
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
func (s *Service) mountTooling(ctx context.Context, userID, sessionID, root string) error {
	// A provider that creates the worktree itself (the docker sandbox) already
	// has it on this path; a provider that has not must not be second-guessed by
	// creating a *host* directory that the sandbox will never see. The old bug
	// this guards against is the host's git touching a container-owned checkout.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		slog.Warn("tooling not mounted: the checkout is not on this filesystem", "root", root)
		return nil
	}
	skills, err := s.cfg.Store.ListSkills(ctx, userID)
	if err != nil {
		return fmt.Errorf("provision: list skills: %w", err)
	}
	servers, err := s.cfg.Store.ListMCPServers(ctx, userID)
	if err != nil {
		return fmt.Errorf("provision: list mcp servers: %w", err)
	}
	if len(skills) == 0 && len(servers) == 0 {
		// Still mount: an empty configuration must clear anything a previous
		// pass wrote, or a removed MCP server keeps running.
	}
	result, err := materialize.Mount(root, skills, servers, materialize.Options{SkillsDir: s.cfg.SkillsDir})
	if err != nil {
		return fmt.Errorf("provision: mount tooling: %w", err)
	}
	for name, reason := range result.Skipped {
		slog.Warn("skill not mounted", "skill", name, "reason", reason)
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
			// bundle: what the agent reads is the fact worth recording.
			digest, derr := materialize.Digest(mounted.Dir)
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
		ExpiresAt: live.ExpiresAt,
		// One key per sandbox, generated where the container was created (or the
		// fixed -sandbox-api-key override). It is sealed by the store and used for
		// every later call to this sandbox's API.
		APIKey: live.SandboxAPIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("provision: record sandbox: %w", err)
	}
	return sb, nil
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
		if filepath.Clean(wt.Path) == filepath.Clean(path) {
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
