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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
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
		return wt, nil
	}

	slug := Slug(branch)
	path := s.cfg.WorktreePath(sb, slug)

	// Reuse an existing row for this path before touching git, so a retry does
	// not trip over "already a worktree".
	if existing, err := s.findWorktreeByPath(ctx, userID, sb.ID, path); err != nil {
		return nil, err
	} else if existing != nil {
		if err := s.attach(ctx, userID, sess.ID, existing.ID); err != nil {
			return nil, err
		}
		return existing, nil
	}

	if err := s.cfg.Git.AddWorktree(ctx, RepoDir(sb), path, branch, true); err != nil {
		return nil, fmt.Errorf("provision: add worktree %s: %w", branch, err)
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
	return wt, nil
}

// ensureSandbox returns the project's sandbox, creating it when absent.
func (s *Service) ensureSandbox(
	ctx context.Context,
	userID string,
	project *contracts.Project,
) (*contracts.Sandbox, error) {
	if sb, err := s.cfg.Store.SandboxForProject(ctx, userID, project.ID); err == nil {
		return sb, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("provision: load sandbox: %w", err)
	}

	live, err := s.cfg.Sandbox.Ensure(ctx, userID, project.ID, project.RepoURL, project.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("provision: create sandbox: %w", err)
	}

	sb, err := s.cfg.Store.CreateSandbox(ctx, userID, store.NewSandbox{
		ProjectID: project.ID,
		State:     live.State,
		Provider:  live.Provider,
		Node:      live.Node,
		Image:     live.Image,
		RootDir:   live.RootDir,
		ExpiresAt: live.ExpiresAt,
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
func BranchFor(project *contracts.Project, sess *contracts.Session) string {
	if sess.Kind != contracts.KindBranch {
		if project.DefaultBranch != "" {
			return project.DefaultBranch
		}
		return "main"
	}
	if sess.Label != "" {
		return "piforge/" + Slug(sess.Label)
	}
	short := sess.ID
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	return "piforge/" + Slug(short)
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
