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

// rangeRepo builds a repository with three commits, each touching a different
// line of the same file, so a range diff of HEAD~1..HEAD and HEAD~2..HEAD can
// only be told apart by what they actually contain.
func rangeRepo(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
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
	write := func() {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write()
	run("add", "-A")
	run("commit", "-m", "seed")

	lines[1] = "line b FIRST"
	write()
	run("commit", "-am", "first")

	lines[20] = "line u SECOND"
	write()
	run("commit", "-am", "second")
	return dir
}

// FR-12.1: a commit range is a diff, not a label. HEAD~1..HEAD must show only
// the last commit's change, and widening the range must change the content.
func TestRangeDiffShowsOnlyTheCommitsInBetween(t *testing.T) {
	dir := rangeRepo(t)
	r := NewRunner()
	ctx := context.Background()

	last, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1", To: "HEAD"})
	if err != nil {
		t.Fatalf("Diff range: %v", err)
	}
	if len(last) != 1 {
		t.Fatalf("files = %d, want 1", len(last))
	}
	if !strings.Contains(last[0].Patch, "line u SECOND") {
		t.Fatalf("the last commit's change is missing:\n%s", last[0].Patch)
	}
	if strings.Contains(last[0].Patch, "line b FIRST") {
		t.Fatalf("HEAD~1..HEAD leaked the previous commit:\n%s", last[0].Patch)
	}

	// Two commits back: both edits, in one patch.
	two, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~2", To: "HEAD"})
	if err != nil {
		t.Fatalf("Diff range HEAD~2..HEAD: %v", err)
	}
	if len(two) != 1 {
		t.Fatalf("files = %d, want 1", len(two))
	}
	for _, want := range []string{"line b FIRST", "line u SECOND"} {
		if !strings.Contains(two[0].Patch, want) {
			t.Fatalf("HEAD~2..HEAD is missing %q:\n%s", want, two[0].Patch)
		}
	}

	// A base branch works as the left side, which is how a review reads a branch.
	branch, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "main", To: "HEAD"})
	if err != nil {
		t.Fatalf("Diff range main..HEAD: %v", err)
	}
	if len(branch) != 0 {
		t.Fatalf("main..HEAD on main itself should be empty, got %+v", branch)
	}

	// An empty range is empty, not an error.
	same, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD", To: "HEAD"})
	if err != nil {
		t.Fatalf("Diff range HEAD..HEAD: %v", err)
	}
	if len(same) != 0 {
		t.Fatalf("HEAD..HEAD = %+v", same)
	}

	// The raw patch follows the same scope.
	patch, err := r.DiffPatch(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1", To: "HEAD"})
	if err != nil {
		t.Fatalf("DiffPatch range: %v", err)
	}
	if !strings.Contains(patch, "line u SECOND") || strings.Contains(patch, "line b FIRST") {
		t.Fatalf("DiffPatch range mismatch:\n%s", patch)
	}

	// Path filtering still applies inside a range.
	none, err := r.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1", To: "HEAD", Path: "other.txt"})
	if err != nil {
		t.Fatalf("Diff range with path: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("a path outside the range returned %+v", none)
	}
}

// A range is two validated revisions, never one string that could smuggle its
// own range (or a flag) into the git command line.
func TestRangeDiffValidatesBothEnds(t *testing.T) {
	dir := rangeRepo(t)
	r := NewRunner()
	ctx := context.Background()

	cases := []struct {
		name string
		req  contracts.DiffRequest
	}{
		{"missing from", contracts.DiffRequest{Scope: contracts.ScopeRange, To: "HEAD"}},
		{"missing to defaults to HEAD", contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD"}},
		{"from is a range", contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1..HEAD", To: "HEAD"}},
		{"to is a range", contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1", To: "HEAD..HEAD"}},
		{"from looks like a flag", contracts.DiffRequest{Scope: contracts.ScopeRange, From: "--output=/tmp/x", To: "HEAD"}},
		{"from has whitespace", contracts.DiffRequest{Scope: contracts.ScopeRange, From: "HEAD~1 --stat", To: "HEAD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Diff(ctx, dir, tc.req)
			if tc.name == "missing to defaults to HEAD" {
				// The right side has a documented default; this one must work.
				if err != nil {
					t.Fatalf("HEAD..HEAD through the default failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}
