package gitx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// WorktreeInfo is one entry of `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
	// Locked/Prunable carry git's own bookkeeping flags when present.
	Locked   bool
	Prunable bool
}

// AddWorktree creates a linked worktree at path.
//
// When createBranch is true a new branch called branch is created from HEAD in
// the new worktree (`git worktree add -b <branch> <path>`). When it is false the
// existing branch is checked out; an empty branch detaches at HEAD.
//
// path is validated as an absolute path (NUL bytes and empty values are
// rejected) and must not live inside repoDir: git refuses nested worktrees and
// a session worktree must stay addressable on its own.
func (r *Runner) AddWorktree(ctx context.Context, repoDir, path, branch string, createBranch bool) error {
	dir, err := repoPath(repoDir)
	if err != nil {
		return err
	}
	abs, err := worktreePath(dir, path)
	if err != nil {
		return err
	}
	branch = strings.TrimSpace(branch)

	args := []string{"worktree", "add"}
	switch {
	case createBranch:
		if branch == "" {
			return fmt.Errorf("gitx: add worktree: branch name required when createBranch is set")
		}
		if err := r.validateBranchName(ctx, dir, branch); err != nil {
			return err
		}
		hasHead, herr := r.HasCommits(ctx, dir)
		if herr != nil {
			return herr
		}
		if !hasHead {
			return fmt.Errorf("gitx: add worktree: repository %s has no commits to branch from", dir)
		}
		args = append(args, "-b", branch, "--", abs)
	case branch != "":
		if err := safeRevArg(branch); err != nil {
			return fmt.Errorf("gitx: add worktree: %w", err)
		}
		args = append(args, "--", abs, branch)
	default:
		args = append(args, "--detach", "--", abs, "HEAD")
	}

	if _, err := r.Run(ctx, dir, args...); err != nil {
		return fmt.Errorf("gitx: add worktree %s: %w", abs, err)
	}
	return nil
}

// RemoveWorktree removes a linked worktree and prunes the bookkeeping.
//
// PRD §6.3-坑1: the worktree has no .git of its own, only a file pointing back
// at <main>/.git/worktrees/<name>. Deleting the directory without
// `git worktree prune` leaves a dead reference behind that later breaks
// branch operations, so this method always runs `worktree remove --force`
// followed by `git worktree prune`, even when the remove itself failed.
func (r *Runner) RemoveWorktree(ctx context.Context, repoDir, path string) error {
	dir, err := repoPath(repoDir)
	if err != nil {
		return err
	}
	abs, err := worktreePath(dir, path)
	if err != nil {
		return err
	}

	removeErr := error(nil)
	if _, rerr := r.Run(ctx, dir, "worktree", "remove", "--force", "--", abs); rerr != nil {
		removeErr = fmt.Errorf("gitx: remove worktree %s: %w", abs, rerr)
	}
	// Always prune: this is the step that clears .git/worktrees/<name> when the
	// directory disappeared underneath us (crash, OOM kill, rm -rf).
	if perr := r.PruneWorktrees(ctx, dir); perr != nil {
		if removeErr != nil {
			return fmt.Errorf("%w (prune also failed: %v)", removeErr, perr)
		}
		return perr
	}
	return removeErr
}

// PruneWorktrees drops administrative entries for worktrees whose directory is
// gone.
func (r *Runner) PruneWorktrees(ctx context.Context, repoDir string) error {
	dir, err := repoPath(repoDir)
	if err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "worktree", "prune", "--expire=now"); err != nil {
		return fmt.Errorf("gitx: prune worktrees %s: %w", dir, err)
	}
	return nil
}

// ListWorktrees parses `git worktree list --porcelain`.
func (r *Runner) ListWorktrees(ctx context.Context, repoDir string) ([]WorktreeInfo, error) {
	dir, err := repoPath(repoDir)
	if err != nil {
		return nil, err
	}
	res, err := r.Run(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("gitx: list worktrees %s: %w", dir, err)
	}
	var (
		out []WorktreeInfo
		cur WorktreeInfo
	)
	flush := func() {
		if cur.Path != "" {
			out = append(out, cur)
		}
		cur = WorktreeInfo{}
	}
	for _, raw := range strings.Split(res.Stdout, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.Locked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			cur.Prunable = true
		}
	}
	flush()
	return out, nil
}

// worktreePath validates a worktree destination.
func worktreePath(repoDir, path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("gitx: worktree: empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrPathEscape, p)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: worktree path %q must be absolute", ErrPathEscape, p)
	}
	abs := filepath.Clean(p)
	if IsInside(repoDir, abs) {
		return "", fmt.Errorf("gitx: worktree %q may not live inside the repository %q", abs, repoDir)
	}
	return abs, nil
}
