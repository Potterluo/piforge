package gitx

import (
	"context"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Regression for the review pane saying "这个文件在当前范围内没有差异" about a
// file that was just written: `git diff` cannot describe a path the index has
// never seen, so a new file had to be reviewed blind.
func TestDiffUnstagedShowsAnUntrackedFile(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	write(t, dir, "src/new.ts", "export const n = 1;\n")

	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{
		Scope: contracts.ScopeUnstaged,
		Path:  "src/new.ts",
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want one file diff, got %d: %+v", len(files), files)
	}
	file := files[0]
	if file.Path != "src/new.ts" {
		t.Errorf("path = %q, want src/new.ts", file.Path)
	}
	if file.Status != "added" {
		t.Errorf("status = %q, want added", file.Status)
	}
	if file.OldPath != "" {
		t.Errorf("old_path = %q, want empty for a new file", file.OldPath)
	}
	if file.Additions != 1 {
		t.Errorf("additions = %d, want 1 (patch: %q)", file.Additions, file.Patch)
	}
	if !strings.Contains(file.Patch, "+export const n = 1;") {
		t.Errorf("patch does not carry the new line: %q", file.Patch)
	}
}

// A tracked file with no worktree change is genuinely unchanged: the untracked
// fallback must not invent a patch for it.
func TestDiffUnstagedStaysEmptyForATrackedUnchangedFile(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{
		Scope: contracts.ScopeUnstaged,
		Path:  "a.txt",
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want no diff for an unchanged tracked file, got %+v", files)
	}
}

// Status reports an untracked directory as a single "dir/" row. Diffing it
// against the null device is an error in git, so the fallback has to refuse it
// rather than surface a failure for a row the panel legitimately lists.
func TestDiffUnstagedIgnoresAnUntrackedDirectory(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	write(t, dir, "pkg/new.go", "package pkg\n")

	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{
		Scope: contracts.ScopeUnstaged,
		Path:  "pkg",
	})
	if err != nil {
		t.Fatalf("diff of an untracked directory must not fail: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want no file diff for a directory, got %+v", files)
	}
}

// The staged scope is a different question (index vs HEAD); a new file that is
// still untracked has nothing staged, and the fallback must not answer it.
func TestDiffStagedDoesNotFallBackToNoIndex(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	write(t, dir, "later.txt", "later\n")

	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{
		Scope: contracts.ScopeStaged,
		Path:  "later.txt",
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want no staged diff for an untracked file, got %+v", files)
	}
}
