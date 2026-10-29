package gitx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Hunk-level operations (PRD FR-12.2).
//
// "Stage this one change" and "throw this one change away" are the two things a person
// reaches for when a run produced four good edits and one bad one. Doing either by
// hand means editing a patch in a terminal, which is exactly the trip this product
// exists to save.
//
// The client never sends a patch: it sends a path and a hunk index, and the patch is
// rebuilt here from the diff the server just produced. A client-supplied patch would be
// a way to write arbitrary content under the guise of "staging".

// Hunk is one contiguous change in a file.
type Hunk struct {
	Index  int    `json:"index"`
	Header string `json:"header"`
	Patch  string `json:"patch"`
}

// Hunks splits one file's diff into hunks that can be addressed by index.
func Hunks(patch string) ([]Hunk, error) {
	lines := strings.Split(patch, "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	hunks := make([]Hunk, 0, 8)
	current := Hunk{}
	var body []string

	flush := func() {
		if current.Header == "" {
			return
		}
		current.Patch = strings.Join(body, "\n")
		if !strings.HasSuffix(current.Patch, "\n") {
			current.Patch += "\n"
		}
		hunks = append(hunks, current)
		body = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			flush()
			current = Hunk{Index: len(hunks), Header: line}
			body = []string{line}
			continue
		}
		if current.Header == "" {
			continue
		}
		// A "\ No newline at end of file" marker belongs to the hunk it follows.
		body = append(body, line)
	}
	flush()
	return hunks, nil
}

// fileHeaderFor builds the a/ b/ preamble a patch needs, for a file that exists in both
// versions. New and deleted files need more care, which is why they are refused below.
func fileHeaderFor(path string) string {
	return "--- a/" + path + "\n+++ b/" + path + "\n"
}

// StageHunk stages one hunk of an unstaged change.
func (r *Runner) StageHunk(ctx context.Context, root, path string, index int) error {
	return r.applyHunk(ctx, root, path, index, contracts.ScopeUnstaged, true)
}

// UnstageHunk takes one hunk back out of the index.
func (r *Runner) UnstageHunk(ctx context.Context, root, path string, index int) error {
	return r.applyHunk(ctx, root, path, index, contracts.ScopeStaged, true)
}

// DiscardHunk throws one hunk's change away, leaving the rest of the file alone.
func (r *Runner) DiscardHunk(ctx context.Context, root, path string, index int) error {
	return r.applyHunk(ctx, root, path, index, contracts.ScopeUnstaged, false)
}

// applyHunk rebuilds one hunk's patch and applies it to the index or the worktree.
func (r *Runner) applyHunk(ctx context.Context, root, path string, index int, scope contracts.DiffScope, toIndex bool) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	clean, err := CleanRel(path)
	if err != nil {
		return err
	}
	diffs, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: scope, Path: clean})
	if err != nil {
		return err
	}
	var target *contracts.FileDiff
	for i := range diffs {
		if diffs[i].Path == clean {
			target = &diffs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("gitx: no %s change in %s", scope, clean)
	}
	if target.Status != "modified" {
		// An added or deleted file is one hunk in practice, and reversing a creation is
		// a different operation (a delete). Refusing is kinder than guessing.
		return fmt.Errorf("gitx: hunk operations need a modified file, %s is %s", clean, target.Status)
	}
	hunks, err := Hunks(target.Patch)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(hunks) {
		return fmt.Errorf("gitx: hunk %d does not exist in %s (%d hunks)", index, clean, len(hunks))
	}

	patch := fileHeaderFor(clean) + hunks[index].Patch
	args := []string{"apply", "--unidiff-zero"}
	if toIndex {
		args = append(args, "--cached")
	}
	if scope == contracts.ScopeStaged && toIndex {
		// Unstaging is the reverse patch, applied to the index only.
		args = append(args, "-R")
	}
	if scope == contracts.ScopeUnstaged && !toIndex {
		// Discarding is the reverse patch, applied to the worktree only.
		args = append(args, "-R")
	}
	args = append(args, "-")
	if _, err := r.runWithInput(ctx, dir, patch, args...); err != nil {
		return fmt.Errorf("gitx: apply hunk %d of %s: %w", index, clean, err)
	}
	return nil
}

// DiscardFile throws away every change in one file: tracked work is restored, a new
// file is deleted. It is the "undo the agent" button, so it is refused for anything the
// index would not be able to restore.
func (r *Runner) DiscardFile(ctx context.Context, root, path string) error {
	dir, err := repoPath(root)
	if err != nil {
		return err
	}
	clean, err := CleanRel(path)
	if err != nil {
		return err
	}
	status, err := r.Status(ctx, dir)
	if err != nil {
		return err
	}
	var index, worktree string
	for _, change := range status.Changes {
		if change.Path == clean {
			index, worktree = change.Index, change.Worktree
			break
		}
	}
	if index == "" && worktree == "" {
		return fmt.Errorf("gitx: %s has no changes to discard", clean)
	}
	// "?" means untracked: git cannot restore it, deleting it is the only way back.
	if strings.Contains(worktree, "?") {
		full := filepath.Join(dir, clean)
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("gitx: delete untracked %s: %w", clean, err)
		}
		return nil
	}
	if _, err := r.Run(ctx, dir, "checkout", "--", clean); err != nil {
		return fmt.Errorf("gitx: discard %s: %w", clean, err)
	}
	return nil
}

// runWithInput runs git with stdin, which is how a patch is applied.
func (r *Runner) runWithInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	res, err := r.RunInput(ctx, dir, []byte(input), args...)
	return res.Stdout, err
}
