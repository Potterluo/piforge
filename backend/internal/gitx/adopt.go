package gitx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Adopt modes. They are part of the API response because "what happened" is the
// question a user asks after pressing adopt: did my branch's commits come over as
// commits, or did only the loose files come over?
const (
	// AdoptModeMerge is a real merge commit on the target: both sides had moved.
	AdoptModeMerge = "merge"
	// AdoptModeFastForward is a clean fast-forward: the target had not moved since
	// the branch forked, so no merge commit was needed.
	AdoptModeFastForward = "fast-forward"
	// AdoptModeWorkingTree means the branch had no commits to give, only
	// uncommitted edits; they were applied to the target's working tree.
	AdoptModeWorkingTree = "working-tree"
)

// Adopt carries a branch's work back to the checkout it came from (PRD FR-2.6).
//
// # Why this is a merge, not a patch
//
// The previous implementation captured the branch's working tree as one commit
// and replayed the patch onto the target. That made "adopt" work only while the
// branch was dirty: the moment a user pressed the product's own commit button,
// there was no working-tree diff left and adopt answered "nothing to adopt". The
// second half of "try a few approaches and pick one" was therefore unreachable
// for exactly the branches people cared about.
//
// Adopting now means "bring this branch's history and this branch's working tree
// into the target", in that order:
//
//  1. If the branch has commits since the merge base, merge them into the target
//     with a real `git merge` in the target worktree. A target that has not moved
//     since the fork is fast-forwarded (no merge commit, linear history); a target
//     that has moved gets `--no-ff`, so the hand-off is one readable commit whose
//     second parent is the branch. Neither path rewrites the base or force-pushes
//     anything.
//  2. If the branch still has uncommitted work, capture it as one commit on the
//     branch (the old behaviour, and the reason the branch keeps a record) and
//     apply that commit's diff to the target's working tree with `git apply
//     --3way`. The target therefore ends dirty, exactly as it did before; the
//     user still has to commit it, which is the deliberate design of the old path.
//
// # Conflicts
//
// A merge conflict is not a 500 and it is not a half-merged target. We collect the
// conflicted paths, run `git merge --abort`, then verify the target is back at the
// exact commit and clean tree it had before the attempt. Only then do we return
// *MergeConflictError. The alternative — leaving the target in a conflicted state —
// was rejected because PiForge has no conflict-resolution UI: the user would be
// left with conflict markers in a worktree they cannot fix from the browser. If
// the rollback itself fails, the error says so explicitly instead of pretending.
//
// A conflict in the *working-tree* half (after the commits merged) rolls back only
// that half: the target keeps the merge result, and the uncommitted edits are not
// applied. That is the honest split, because the commits were already delivered;
// undoing them would make a partially-successful adopt look like a total failure.
func (r *Runner) Adopt(ctx context.Context, sourceDir, targetDir, message string) (contracts.AdoptResult, error) {
	if strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(targetDir) == "" {
		return contracts.AdoptResult{}, errors.New("gitx: adopt needs two worktrees")
	}

	sourceStatus, err := r.runStatus(ctx, sourceDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	targetStatus, err := r.runStatus(ctx, targetDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	// Refuse before touching anything: a merge or a 3-way apply on top of someone's
	// uncommitted edits is how work disappears silently.
	if !targetStatus.Clean {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: the target checkout has uncommitted changes: %w", ErrTargetDirty)
	}

	sourceHead, err := r.Head(ctx, sourceDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	targetHead, err := r.Head(ctx, targetDir)
	if err != nil {
		return contracts.AdoptResult{}, err
	}
	if targetHead == "" {
		// Nothing to merge onto. The old patch path also needed a commit to apply a
		// 3-way patch against, so this is the same refusal, said earlier.
		return contracts.AdoptResult{}, fmt.Errorf("gitx: the target checkout has no commits yet: %w", ErrNothingToAdopt)
	}

	result := contracts.AdoptResult{Files: []string{}, Commits: []string{}}
	fileSet := make(map[string]bool, 8)
	addFiles := func(paths []string) {
		for _, path := range paths {
			if path = strings.TrimSpace(path); path != "" {
				fileSet[path] = true
			}
		}
	}

	broughtCommits := false
	if sourceHead != "" {
		base, berr := r.mergeBase(ctx, targetDir, targetHead, sourceHead)
		if berr != nil {
			return contracts.AdoptResult{}, fmt.Errorf("gitx: adopt: %w: %v", ErrNoCommonBase, berr)
		}
		ahead, aerr := r.revListCount(ctx, sourceDir, base, sourceHead)
		if aerr != nil {
			return contracts.AdoptResult{}, aerr
		}
		if ahead > 0 {
			commits, lerr := r.revList(ctx, sourceDir, base, sourceHead)
			if lerr != nil {
				return contracts.AdoptResult{}, lerr
			}
			mode, mergeCommit, changed, merr := r.adoptCommits(ctx, sourceDir, targetDir, sourceHead, base, targetHead, message)
			if merr != nil {
				return contracts.AdoptResult{}, merr
			}
			result.Mode = mode
			result.MergeCommit = mergeCommit
			result.Commits = commits
			broughtCommits = true
			addFiles(changed)
		}
	}

	if !sourceStatus.Clean {
		branchCommit, changed, werr := r.adoptWorkingTree(ctx, sourceDir, targetDir, message)
		switch {
		case errors.Is(werr, ErrNothingToCommit):
			// Status reported something git will not commit (a submodule, or a file
			// that vanished between the two commands). Nothing to add.
		case werr != nil:
			return contracts.AdoptResult{}, werr
		default:
			result.BranchCommit = branchCommit
			addFiles(changed)
			if result.Mode == "" {
				result.Mode = AdoptModeWorkingTree
			} else {
				result.Mode += "+" + AdoptModeWorkingTree
			}
		}
	}

	if result.Mode == "" {
		return contracts.AdoptResult{}, fmt.Errorf("gitx: this branch has no commits on top of the base and no uncommitted work: %w", ErrNothingToAdopt)
	}

	// Commit is the useful "what do I look at" id: the target's new HEAD when
	// commits travelled, and the branch-side commit for a working-tree-only adopt
	// (the target's HEAD did not move in that case).
	if broughtCommits {
		head, herr := r.Head(ctx, targetDir)
		if herr != nil {
			return contracts.AdoptResult{}, herr
		}
		result.Commit = head
	} else {
		result.Commit = result.BranchCommit
	}
	result.Files = sortedKeys(fileSet)
	return result, nil
}

// adoptCommits merges the branch's commits into the target.
//
// Fast-forward when the target has not moved since the merge base (linear history,
// nothing to reconcile); otherwise a real merge commit. The merge runs in the
// target worktree, never as a ref rewrite, so the target branch's history stays
// readable and the base is never forced.
func (r *Runner) adoptCommits(ctx context.Context, sourceDir, targetDir, sourceHead, base, targetHead, message string) (mode, mergeCommit string, files []string, err error) {
	// Prefer the branch name for a readable merge; a detached checkout merges by
	// commit id, which git accepts just as well.
	ref := sourceHead
	if branch, berr := r.CurrentBranch(ctx, sourceDir); berr == nil && strings.TrimSpace(branch) != "" {
		if ok, rerr := r.RefExists(ctx, sourceDir, branch); rerr == nil && ok {
			ref = branch
		}
	}

	fastForward := targetHead == base
	args := append([]string{}, r.identityArgs(ctx, targetDir)...)
	args = append(args, "merge")
	if fastForward {
		args = append(args, "--ff-only")
	} else {
		args = append(args, "--no-ff", "--no-edit")
		if strings.TrimSpace(message) == "" {
			message = "adopt: merge a branch"
		}
		args = append(args, "-m", message)
	}
	args = append(args, ref)

	if _, rerr := r.Run(ctx, targetDir, args...); rerr != nil {
		conflicted := r.conflictedPaths(ctx, targetDir)
		if len(conflicted) > 0 {
			// Roll the merge back and prove the target is untouched. With a clean
			// target, `git merge --abort` is git's own documented undo.
			if aerr := r.abortMerge(ctx, targetDir); aerr != nil {
				return "", "", nil, fmt.Errorf("%w; rolling the merge back failed (%v): the target may still be mid-merge — run `git merge --abort` there before retrying", &MergeConflictError{Files: conflicted, Stage: "commits"}, aerr)
			}
			head, herr := r.Head(ctx, targetDir)
			st, serr := r.runStatus(ctx, targetDir)
			if herr != nil || serr != nil || head != targetHead || st == nil || !st.Clean {
				return "", "", nil, fmt.Errorf("%w; the merge was aborted but the target could not be verified clean — check it before continuing", &MergeConflictError{Files: conflicted, Stage: "commits"})
			}
			return "", "", nil, &MergeConflictError{Files: conflicted, Stage: "commits"}
		}
		return "", "", nil, fmt.Errorf("gitx: adopt: merge %s: %w", ref, rerr)
	}

	newHead, herr := r.Head(ctx, targetDir)
	if herr != nil {
		return "", "", nil, herr
	}
	changed, derr := r.changedPaths(ctx, targetDir, targetHead, newHead)
	if derr != nil {
		return "", "", nil, derr
	}
	if fastForward {
		return AdoptModeFastForward, "", changed, nil
	}
	return AdoptModeMerge, newHead, changed, nil
}

// adoptWorkingTree is the old adopt path, kept intact for branches whose work is
// not committed yet: one commit on the branch for the record, then a 3-way apply of
// that commit onto the target's working tree.
func (r *Runner) adoptWorkingTree(ctx context.Context, sourceDir, targetDir, message string) (string, []string, error) {
	if _, err := r.Run(ctx, sourceDir, "add", "-A"); err != nil {
		return "", nil, fmt.Errorf("gitx: adopt: stage: %w", err)
	}
	if message == "" {
		message = "adopt: changes from a branch"
	}
	// The commit uses the same identity fallback as `Commit`: without it, a fresh
	// container has no `user.email` and every adopt failed with "Author identity
	// unknown".
	commitArgs := append(r.identityArgs(ctx, sourceDir), "commit", "-m", message, "--no-verify")
	if _, err := r.Run(ctx, sourceDir, commitArgs...); err != nil {
		if classified := classifyCommit(err); errors.Is(classified, ErrNothingToCommit) {
			return "", nil, classified
		}
		return "", nil, fmt.Errorf("gitx: adopt: commit: %w", err)
	}

	head, err := r.Head(ctx, sourceDir)
	if err != nil {
		return "", nil, fmt.Errorf("gitx: adopt: read head: %w", err)
	}
	patch, err := r.Run(ctx, sourceDir, "show", "--format=%H", "--patch", "HEAD")
	if err != nil {
		return "", nil, fmt.Errorf("gitx: adopt: read the patch: %w", err)
	}

	// The apply must not leave a half-applied target behind. `git apply --3way`
	// refuses atomically on a plain failure, but on a real conflict it writes
	// conflict markers and unmerged index entries; that is the case `restoreTarget`
	// exists for. The target was verified clean at the start, so the pre-apply HEAD
	// is a safe restore point.
	preApplyHead, herr := r.Head(ctx, targetDir)
	if herr != nil {
		return "", nil, fmt.Errorf("gitx: adopt: read target head: %w", herr)
	}
	if _, aerr := r.RunInput(ctx, targetDir, []byte(patch.Stdout), "apply", "--3way", "--whitespace=nowarn"); aerr != nil {
		conflicted := r.conflictedPaths(ctx, targetDir)
		if len(conflicted) > 0 {
			if rerr := r.restoreTarget(ctx, targetDir, preApplyHead); rerr != nil {
				return "", nil, fmt.Errorf("%w; restoring the target failed (%v): inspect it before continuing", &MergeConflictError{Files: conflicted, Stage: "working-tree"}, rerr)
			}
			return "", nil, &MergeConflictError{Files: conflicted, Stage: "working-tree"}
		}
		return "", nil, fmt.Errorf("gitx: adopt: apply: %w", aerr)
	}

	files, ferr := r.showFiles(ctx, sourceDir, head)
	if ferr != nil {
		// The work is already in the target; failing now would report a failure that
		// did happen. Report the commit without a file list instead.
		return head, nil, nil
	}
	return head, files, nil
}

// MergeConflictError reports a refused adopt and which files overlapped. Stage is
// "commits" when the merge itself conflicted (nothing changed in the target) and
// "working-tree" when only the uncommitted half conflicted (commits were merged).
type MergeConflictError struct {
	Files []string
	Stage string
}

// Error implements error.
func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("gitx: adopt conflict in %s (%s)", strings.Join(e.Files, ", "), e.Stage)
}

// Adopt errors, exported so the HTTP layer can answer 409 instead of 500.
var (
	ErrNothingToAdopt = errors.New("nothing to adopt")
	ErrTargetDirty    = errors.New("target checkout is dirty")
	// ErrNoCommonBase means the two checkouts do not share history, so no diff can
	// be computed and no merge would mean anything.
	ErrNoCommonBase = errors.New("the two checkouts have no common history")
)

func (r *Runner) runStatus(ctx context.Context, dir string) (*contracts.GitStatus, error) {
	return r.Status(ctx, dir)
}

// mergeBase returns the common ancestor of two commits.
func (r *Runner) mergeBase(ctx context.Context, dir, a, b string) (string, error) {
	res, err := r.Run(ctx, dir, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(res.Stdout)
	if base == "" {
		return "", fmt.Errorf("git merge-base %s %s produced no commit", a, b)
	}
	return base, nil
}

// revListCount counts commits in base..head.
func (r *Runner) revListCount(ctx context.Context, dir, base, head string) (int, error) {
	res, err := r.Run(ctx, dir, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, fmt.Errorf("gitx: adopt: count commits: %w", err)
	}
	n, perr := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if perr != nil {
		return 0, fmt.Errorf("gitx: adopt: count commits: %w", perr)
	}
	return n, nil
}

// revList lists base..head oldest first, so the response reads like history.
func (r *Runner) revList(ctx context.Context, dir, base, head string) ([]string, error) {
	res, err := r.Run(ctx, dir, "rev-list", "--reverse", base+".."+head)
	if err != nil {
		return nil, fmt.Errorf("gitx: adopt: list commits: %w", err)
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(res.Stdout, "\n") {
		if sha := strings.TrimSpace(line); sha != "" {
			out = append(out, sha)
		}
	}
	return out, nil
}

// changedPaths lists the paths that differ between two commits.
func (r *Runner) changedPaths(ctx context.Context, dir, from, to string) ([]string, error) {
	res, err := r.Run(ctx, dir, "diff", "--name-only", from, to)
	if err != nil {
		return nil, fmt.Errorf("gitx: adopt: list changed files: %w", err)
	}
	return splitLines(res.Stdout), nil
}

// showFiles lists the paths a commit touched.
func (r *Runner) showFiles(ctx context.Context, dir, commit string) ([]string, error) {
	res, err := r.Run(ctx, dir, "show", "--name-only", "--format=", commit)
	if err != nil {
		return nil, err
	}
	return splitLines(res.Stdout), nil
}

// conflictedPaths returns the unmerged paths of a stopped merge or a conflicted
// 3-way apply. It is the only signal used to decide "this was a conflict" versus
// "this failed for another reason", so it must not invent paths.
func (r *Runner) conflictedPaths(ctx context.Context, dir string) []string {
	res, err := r.Run(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	paths := splitLines(res.Stdout)
	if len(paths) > 0 {
		return paths
	}
	// Some 3-way apply failures leave the unmerged entries only in the index.
	ls, lerr := r.Run(ctx, dir, "ls-files", "-u", "-z")
	if lerr != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, record := range strings.Split(ls.Stdout, "\x00") {
		// "<mode> <sha> <stage>\t<path>"
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		path := record[tab+1:]
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// abortMerge undoes a merge that stopped on a conflict.
func (r *Runner) abortMerge(ctx context.Context, dir string) error {
	if _, err := r.Run(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
		// The merge never started (or already cleaned up); there is nothing to undo.
		return nil
	}
	if _, err := r.Run(ctx, dir, "merge", "--abort"); err != nil {
		return err
	}
	return nil
}

// restoreTarget puts a checkout back at head and removes what a failed apply left
// behind. The caller has already proven the target was clean at head, so this only
// discards this operation's own partial output.
func (r *Runner) restoreTarget(ctx context.Context, dir, head string) error {
	if _, err := r.Run(ctx, dir, "reset", "--hard", head); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "clean", "-fdq"); err != nil {
		return err
	}
	got, err := r.Head(ctx, dir)
	if err != nil {
		return err
	}
	if got != head {
		return fmt.Errorf("target HEAD is %s, want %s", got, head)
	}
	st, err := r.runStatus(ctx, dir)
	if err != nil {
		return err
	}
	if !st.Clean {
		return fmt.Errorf("target is still dirty after restore")
	}
	return nil
}

func splitLines(out string) []string {
	lines := make([]string, 0, 8)
	for _, raw := range strings.Split(out, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
