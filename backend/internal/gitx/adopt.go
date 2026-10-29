package gitx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Adopt carries one worktree's uncommitted work into another (PRD FR-2.6).
//
// The shape is deliberate: the branch's changes are captured as a *commit* first, so
// the patch has a stable base even if the user keeps editing, and then replayed onto
// the target. That is a cherry-pick without needing the two worktrees to share a
// branch, which git worktrees of the same repository do not.
//
// It refuses rather than guesses in the two cases where a silent merge would lose
// work: the source has nothing to adopt, and the target has uncommitted changes.
func (r *Runner) Adopt(ctx context.Context, sourceDir, targetDir, message string) (contracts.AdoptResult, error) {
	if strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(targetDir) == "" {
		return contracts.AdoptResult{}, errors.New("gitx: adopt needs two worktrees")
	}

	sourceStatus, err := r.runStatus(ctx, sourceDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	if sourceStatus.Clean {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: this branch has no uncommitted work to adopt: %w", ErrNothingToAdopt)
	}

	targetStatus, err := r.runStatus(ctx, targetDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	if !targetStatus.Clean {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: the target checkout has uncommitted changes: %w", ErrTargetDirty)
	}

	// Capture the branch's work as a commit on its own HEAD, so the patch is stable
	// and the branch itself keeps a record of what was adopted.
	if _, err := r.Run(ctx, sourceDir, "add", "-A"); err != nil {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: stage: %w", err)
	}
	if message == "" {
		message = "adopt: changes from a branch"
	}
	// The commit uses the same identity fallback as `Commit`: without it, a fresh
	// container has no `user.email` and every adopt failed with "Author identity
	// unknown" — a 500 the user cannot act on.
	commitArgs := append(r.identityArgs(ctx, sourceDir), "commit", "-m", message, "--no-verify")
	if _, err := r.Run(ctx, sourceDir, commitArgs...); err != nil {
		// A commit that produced nothing means the tree matched HEAD after staging;
		// that is still "nothing to adopt", not a failure.
		if strings.Contains(StderrOf(err), "nothing to commit") {
			return contracts.AdoptResult{}, fmt.Errorf("gitx: this branch has no changes against its base: %w", ErrNothingToAdopt)
		}
		return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: commit: %w", err)
	}

	// Replay that commit onto the target with a three-way apply, which handles the
	// common case (the target advanced while the branch was open) without a merge
	// driver.
	patch, err := r.Run(ctx, sourceDir, "show", "--format=%H", "--patch", "HEAD")
	if err != nil {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: read the patch: %w", err)
	}
	if _, err := r.RunInput(ctx, targetDir, []byte(patch.Stdout), "apply", "--3way", "--whitespace=nowarn"); err != nil {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: apply: %w", err)
	}

	head, err := r.Run(ctx, sourceDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: read head: %w", err)
	}
	files, perr := r.Run(ctx, sourceDir, "show", "--name-only", "--format=", "HEAD")
	result := contracts.AdoptResult{Commit: strings.TrimSpace(head.Stdout)}
	if perr == nil {
		for _, line := range strings.Split(files.Stdout, "\n") {
			if path := strings.TrimSpace(line); path != "" {
				result.Files = append(result.Files, path)
			}
		}
	}
	return result, nil
}

// Adopt errors, exported so the HTTP layer can answer 409 instead of 500.
var (
	ErrNothingToAdopt = errors.New("nothing to adopt")
	ErrTargetDirty    = errors.New("target checkout is dirty")
)

func (r *Runner) runStatus(ctx context.Context, dir string) (*contracts.GitStatus, error) {
	return r.Status(ctx, dir)
}
