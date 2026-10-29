package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FR-2.6: carrying a branch's work back to the checkout it came from. The cases that
// matter are the happy path and the two refusals — a silent merge in either refusal
// case loses someone's work.
func TestAdoptCarriesWorkAndRefusesLossyCases(t *testing.T) {
	if testing.Short() {
		t.Skip("uses real git")
	}
	runner := NewRunner()
	ctx := context.Background()

	root, source, target := initRepoWithTwoWorktrees(t, runner, ctx)

	// Nothing to adopt on a clean branch.
	if _, err := runner.Adopt(ctx, source, target, "adopt"); !errors.Is(err, ErrNothingToAdopt) {
		t.Fatalf("adopt on a clean branch = %v, want ErrNothingToAdopt", err)
	}

	// A change on the branch is carried to the target.
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("from the branch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Adopt(ctx, source, target, "adopt: feature")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Commit == "" {
		t.Fatal("no commit id reported")
	}
	raw, err := os.ReadFile(filepath.Join(target, "feature.txt"))
	if err != nil {
		t.Fatalf("the adopted file is not in the target: %v", err)
	}
	// Line endings are git's business (core.autocrlf on Windows rewrites them); the
	// content is what this test is about.
	if strings.TrimSpace(string(raw)) != "from the branch" {
		t.Fatalf("target file = %q", raw)
	}
	if len(result.Files) == 0 || result.Files[0] != "feature.txt" {
		t.Fatalf("files = %v", result.Files)
	}

	// A dirty target is refused: applying on top of someone's uncommitted edits is
	// how work disappears.
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "another.txt"), []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Adopt(ctx, source, target, "adopt: another"); !errors.Is(err, ErrTargetDirty) {
		t.Fatalf("adopt onto a dirty target = %v, want ErrTargetDirty", err)
	}
	_ = root
}

// initRepoWithTwoWorktrees builds a repository with a main checkout and one linked
// worktree, which is exactly the shape the product gives a branch.
func initRepoWithTwoWorktrees(t *testing.T, r *Runner, ctx context.Context) (root, source, target string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(dir string, args ...string) {
		if _, err := r.Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, StderrOf(err))
		}
	}
	must(root, "init", "-b", "main")
	// No user.email/user.name on purpose: a fresh container has none, and the adopt
	// must supply its own fallback rather than fail with "Author identity unknown".
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must(root, "add", "-A")
	must(root, "commit", "-m", "init")

	source = filepath.Join(base, "branch")
	must(root, "worktree", "add", "-b", "branch", source, "HEAD")

	// The target is the main checkout, which is where an adopt lands.
	return root, source, root
}
