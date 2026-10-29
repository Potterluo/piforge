package gitx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Stage adds the given repository-relative paths to the index. An empty path
// list stages the whole worktree (equivalent to `git add -A`).
func (r *Runner) Stage(ctx context.Context, root string, paths []string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	args := []string{"add", "-A"}
	rels, err := cleanRels(paths)
	if err != nil {
		return err
	}
	if len(rels) > 0 {
		args = append(args, "--")
		args = append(args, rels...)
	}
	if _, err := r.Run(ctx, dir, args...); err != nil {
		return fmt.Errorf("gitx: stage %s: %w", dir, err)
	}
	return nil
}

// Unstage removes the given paths from the index, leaving the worktree alone.
// On a repository without commits it falls back to `git rm --cached`.
func (r *Runner) Unstage(ctx context.Context, root string, paths []string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	rels, err := cleanRels(paths)
	if err != nil {
		return err
	}
	hasHead, err := r.HasCommits(ctx, dir)
	if err != nil {
		return err
	}
	var args []string
	if hasHead {
		args = append(args, "reset", "-q", "HEAD")
		if len(rels) > 0 {
			args = append(args, "--")
			args = append(args, rels...)
		}
	} else {
		args = append(args, "rm", "-r", "--cached", "--ignore-unmatch", "-q")
		if len(rels) > 0 {
			args = append(args, "--")
			args = append(args, rels...)
		} else {
			args = append(args, "--", ".")
		}
	}
	if _, err := r.Run(ctx, dir, args...); err != nil {
		return fmt.Errorf("gitx: unstage %s: %w", dir, err)
	}
	return nil
}

// Commit records the staged tree and returns the new HEAD hash.
//
// When req.Paths is non-empty those paths are staged first, which is what the
// Git panel does ("commit only the checked files"). The committed tree is
// whatever is staged afterwards.
func (r *Runner) Commit(ctx context.Context, root string, req contracts.CommitRequest) (string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return "", err
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		return "", fmt.Errorf("gitx: commit %s: empty commit message", dir)
	}
	if len(req.Paths) > 0 {
		if err := r.Stage(ctx, dir, req.Paths); err != nil {
			return "", err
		}
	}

	args := append(r.identityArgs(ctx, dir), "commit", "-m", msg)
	if req.Amend {
		args = append(args, "--amend")
	}
	if _, err := r.Run(ctx, dir, args...); err != nil {
		if classified := classifyCommit(err); errors.Is(classified, ErrNothingToCommit) {
			return "", classified
		}
		return "", fmt.Errorf("gitx: commit %s: %w", dir, err)
	}
	head, err := r.Head(ctx, dir)
	if err != nil {
		return "", err
	}
	return head, nil
}

// identityArgs supplies a fallback committer identity when the host has none
// configured, so that commits do not fail with "please tell me who you are".
func (r *Runner) identityArgs(ctx context.Context, dir string) []string {
	var args []string
	if strings.TrimSpace(r.configGet(ctx, dir, "user.name")) == "" {
		args = append(args, "-c", "user.name=PiForge")
	}
	if strings.TrimSpace(r.configGet(ctx, dir, "user.email")) == "" {
		args = append(args, "-c", "user.email=piforge@localhost")
	}
	return args
}

func (r *Runner) configGet(ctx context.Context, dir, key string) string {
	res, err := r.Run(ctx, dir, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// CreateBranch creates a branch at HEAD without switching to it.
func (r *Runner) CreateBranch(ctx context.Context, root, name string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	if err := r.validateBranchName(ctx, dir, name); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "branch", "--", name); err != nil {
		return fmt.Errorf("gitx: create branch %q: %w", name, err)
	}
	return nil
}

// Checkout switches the working tree to an existing branch.
func (r *Runner) Checkout(ctx context.Context, root, name string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	if err := safeRevArg(name); err != nil {
		return fmt.Errorf("gitx: checkout: %w", err)
	}
	ok, err := r.RefExists(ctx, dir, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("gitx: checkout: unknown branch %q", name)
	}
	if _, err := r.Run(ctx, dir, "checkout", "-q", name); err != nil {
		return fmt.Errorf("gitx: checkout %q: %w", name, err)
	}
	return nil
}

// CreateAndCheckout creates a branch at HEAD and switches to it.
//
// `git checkout -b` rather than branch-then-checkout, because a freshly cloned **empty**
// repository has no commit to branch from: `git branch -- main` fails with "not a valid
// object name", and that turned every project created from an empty remote into a
// session with no checkout at all.
func (r *Runner) CreateAndCheckout(ctx context.Context, root, name string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	if err := r.validateBranchName(ctx, dir, name); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "checkout", "-q", "-b", name); err != nil {
		return fmt.Errorf("gitx: create branch %q: %w", name, err)
	}
	return nil
}

// RefExists reports whether a revision resolves to a commit.
func (r *Runner) RefExists(ctx context.Context, root, ref string) (bool, error) {
	dir, err := repoPath(root)
	if err != nil {
		return false, err
	}
	if err := safeRevArg(ref); err != nil {
		return false, fmt.Errorf("gitx: ref: %w", err)
	}
	res, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if ExitCode(err) == 1 || ExitCode(err) == 128 {
			return false, nil
		}
		return false, fmt.Errorf("gitx: ref %q: %w", ref, err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// BranchExists reports whether a local branch exists.
func (r *Runner) BranchExists(ctx context.Context, root, name string) (bool, error) {
	dir, err := repoPath(root)
	if err != nil {
		return false, err
	}
	if err := safeRevArg(name); err != nil {
		return false, fmt.Errorf("gitx: branch: %w", err)
	}
	res, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil {
		if ExitCode(err) == 1 || ExitCode(err) == 128 {
			return false, nil
		}
		return false, fmt.Errorf("gitx: branch %q: %w", name, err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// Init creates a repository at dir with the given initial branch name.
func (r *Runner) Init(ctx context.Context, dir, branch string) error {
	abs, err := absClean(dir)
	if err != nil {
		return err
	}
	args := []string{"init", "-q"}
	if strings.TrimSpace(branch) != "" {
		args = append(args, "-b", branch)
	}
	if _, err := r.Run(ctx, abs, args...); err != nil {
		return fmt.Errorf("gitx: init %s: %w", abs, err)
	}
	return nil
}

// Clone clones repoURL into dir. The destination must be empty or absent.
func (r *Runner) Clone(ctx context.Context, repoURL, dir, branch string) error {
	abs, err := absClean(dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(repoURL) == "" {
		return fmt.Errorf("gitx: clone: empty repository URL")
	}
	args := []string{"clone", "--quiet"}
	if b := strings.TrimSpace(branch); b != "" {
		args = append(args, "--branch", b)
	}
	args = append(args, "--", repoURL, abs)
	if _, err := r.Run(ctx, "", args...); err != nil {
		return fmt.Errorf("gitx: clone %s: %w", repoURL, err)
	}
	return nil
}

// CommitEmpty creates an empty commit, which is what bootstraps a `git init`
// repository so that `git worktree add` has a commit to branch from.
func (r *Runner) CommitEmpty(ctx context.Context, dir, message string) error {
	abs, err := absClean(dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		message = "piforge: initial commit"
	}
	args := append(r.identityArgs(ctx, abs), "commit", "--allow-empty", "--no-gpg-sign", "-m", message)
	if _, err := r.Run(ctx, abs, args...); err != nil {
		return fmt.Errorf("gitx: empty commit %s: %w", abs, err)
	}
	return nil
}

// SetRemote adds or replaces the "origin" remote.
func (r *Runner) SetRemote(ctx context.Context, dir, url string) error {
	abs, err := absClean(dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("gitx: remote: empty URL")
	}
	if _, err := r.Run(ctx, abs, "remote", "add", "origin", "--", url); err != nil {
		if _, serr := r.Run(ctx, abs, "remote", "set-url", "origin", "--", url); serr != nil {
			return fmt.Errorf("gitx: set remote %s: %w", url, err)
		}
	}
	return nil
}

// validateBranchName asks git itself whether name is a legal branch name.
func (r *Runner) validateBranchName(ctx context.Context, dir, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("gitx: empty branch name")
	}
	if err := safeRevArg(name); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("gitx: invalid branch name %q: %w", name, err)
	}
	return nil
}

// cleanRels validates and cleans every caller-supplied path.
func cleanRels(paths []string) ([]string, error) {
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := CleanRel(p)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			continue
		}
		rels = append(rels, rel)
	}
	return rels, nil
}

// ErrNothingToCommit is returned when git has nothing staged, so the HTTP layer can
// answer "nothing to commit" instead of a 500 with git's own wording inside it.
var ErrNothingToCommit = errors.New("gitx: nothing to commit")

// classifyCommit turns git's "nothing to commit" into a sentinel.
func classifyCommit(err error) error {
	if strings.Contains(StderrOf(err)+err.Error(), "nothing to commit") {
		return ErrNothingToCommit
	}
	return err
}
