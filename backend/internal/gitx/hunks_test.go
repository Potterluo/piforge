package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// helper: a repository with one committed file and two separate edits in it, so there
// are genuinely two hunks to address independently.
func hunkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	lines := make([]string, 24)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i))
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "seed")

	// Two edits far apart: two hunks.
	lines[1] = "line b EDITED"
	lines[20] = "line u EDITED"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// FR-12.2: staging one hunk must stage that hunk and nothing else.
func TestStageOneHunkLeavesTheOtherAlone(t *testing.T) {
	dir := hunkRepo(t)
	r := NewRunner()
	ctx := context.Background()

	diffs, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("diffs = %d", len(diffs))
	}
	hunks, err := Hunks(diffs[0].Patch)
	if err != nil {
		t.Fatalf("Hunks: %v", err)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2\n%s", len(hunks), diffs[0].Patch)
	}

	if err := r.StageHunk(ctx, dir, "f.txt", 0); err != nil {
		t.Fatalf("StageHunk: %v", err)
	}

	staged, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeStaged})
	if err != nil {
		t.Fatalf("staged Diff: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged files = %d", len(staged))
	}
	if got := strings.Count(staged[0].Patch, "EDITED"); got != 1 {
		t.Fatalf("the staged patch has %d edits, want 1:\n%s", got, staged[0].Patch)
	}
	// The other hunk is still there, unstaged.
	unstaged, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil {
		t.Fatalf("unstaged Diff: %v", err)
	}
	if len(unstaged) != 1 || !strings.Contains(unstaged[0].Patch, "line u EDITED") {
		t.Fatalf("the second hunk did not survive:\n%v", unstaged)
	}
}

// Discarding one hunk must not discard the file.
func TestDiscardOneHunkKeepsTheOther(t *testing.T) {
	dir := hunkRepo(t)
	r := NewRunner()
	ctx := context.Background()

	if err := r.DiscardHunk(ctx, dir, "f.txt", 1); err != nil {
		t.Fatalf("DiscardHunk: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "line u EDITED") {
		t.Fatal("the discarded hunk is still in the file")
	}
	if !strings.Contains(string(body), "line b EDITED") {
		t.Fatal("the hunk that should have survived is gone")
	}
}

// Discarding a whole file: tracked work is restored, a new file is deleted.
func TestDiscardFileHandlesTrackedAndNewFiles(t *testing.T) {
	dir := hunkRepo(t)
	r := NewRunner()
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.DiscardFile(ctx, dir, "new.txt"); err != nil {
		t.Fatalf("discard a new file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("the untracked file was not deleted")
	}

	if err := r.DiscardFile(ctx, dir, "f.txt"); err != nil {
		t.Fatalf("discard a tracked file: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "EDITED") {
		t.Fatal("the tracked file still has its edits")
	}
	// And a second discard of an unchanged file is refused, not a silent no-op.
	if err := r.DiscardFile(ctx, dir, "f.txt"); err == nil {
		t.Fatal("discarding an unchanged file was accepted")
	}
}

// A hunk index the server does not have is an error, not an applied patch.
func TestHunkIndexIsValidated(t *testing.T) {
	dir := hunkRepo(t)
	r := NewRunner()
	ctx := context.Background()
	if err := r.StageHunk(ctx, dir, "f.txt", 99); err == nil {
		t.Fatal("an out-of-range hunk index was accepted")
	}
	if err := r.DiscardHunk(ctx, dir, "../escape.txt", 0); err == nil {
		t.Fatal("a path escaping the repository was accepted")
	}
}
