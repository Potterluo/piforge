package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-2.6: carrying a branch's work back to the checkout it came from. The cases that
// matter are the happy paths — a branch whose work is uncommitted, one with commits,
// and one with both — plus the two refusals where a silent merge would lose work.
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
	if result.Mode != AdoptModeWorkingTree {
		t.Fatalf("mode = %q, want %q", result.Mode, AdoptModeWorkingTree)
	}
	if result.BranchCommit == "" || result.Commit != result.BranchCommit {
		t.Fatalf("working-tree adopt did not report its branch commit: %+v", result)
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

// The bug this covers: after the user pressed the product's own commit button the
// branch had no working-tree diff left, and adopt answered "nothing to adopt". A
// committed branch must fast-forward the target and the commit must be reachable.
func TestAdoptFastForwardsCommittedBranch(t *testing.T) {
	runner := NewRunner()
	ctx := context.Background()
	_, source, target := initRepoWithTwoWorktrees(t, runner, ctx)

	if err := os.WriteFile(filepath.Join(source, "committed.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchHead := commitAll(t, runner, ctx, source, "the agent committed this")

	result, err := runner.Adopt(ctx, source, target, "adopt: committed")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Mode != AdoptModeFastForward {
		t.Fatalf("mode = %q, want %q (%+v)", result.Mode, AdoptModeFastForward, result)
	}
	if len(result.Commits) != 1 || result.Commits[0] != branchHead {
		t.Fatalf("commits = %v, want [%s]", result.Commits, branchHead)
	}
	if result.MergeCommit != "" {
		t.Fatalf("a fast-forward created a merge commit: %q", result.MergeCommit)
	}
	if result.Commit != branchHead {
		t.Fatalf("commit = %q, want the branch head %q", result.Commit, branchHead)
	}
	if len(result.Files) == 0 || result.Files[0] != "committed.txt" {
		t.Fatalf("files = %v", result.Files)
	}
	assertFile(t, filepath.Join(target, "committed.txt"), "one\ntwo\n")
	head, err := runner.Head(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if head != branchHead {
		t.Fatalf("target head = %s, want the fast-forwarded %s", head, branchHead)
	}
	assertClean(t, runner, ctx, target)
}

// A target that moved since the fork cannot fast-forward; the hand-off must be one
// real merge commit whose second parent is the adopted branch.
func TestAdoptCreatesAMergeCommitWhenTheTargetMoved(t *testing.T) {
	runner := NewRunner()
	ctx := context.Background()
	_, source, target := initRepoWithTwoWorktrees(t, runner, ctx)

	if err := os.WriteFile(filepath.Join(target, "trunk.txt"), []byte("trunk work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitAll(t, runner, ctx, target, "work on the trunk")

	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("branch work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchHead := commitAll(t, runner, ctx, source, "work on the branch")

	result, err := runner.Adopt(ctx, source, target, "adopt: merge the branch")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Mode != AdoptModeMerge {
		t.Fatalf("mode = %q, want %q (%+v)", result.Mode, AdoptModeMerge, result)
	}
	if result.MergeCommit == "" || result.Commit != result.MergeCommit {
		t.Fatalf("no merge commit reported: %+v", result)
	}
	if len(result.Commits) != 1 || result.Commits[0] != branchHead {
		t.Fatalf("commits = %v", result.Commits)
	}
	assertFile(t, filepath.Join(target, "feature.txt"), "branch work\n")
	assertFile(t, filepath.Join(target, "trunk.txt"), "trunk work\n")
	assertClean(t, runner, ctx, target)

	// Two parents, and the second one is the branch: that is what "reachable" means.
	parents, err := runner.Run(ctx, target, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(parents.Stdout)
	if len(fields) != 3 {
		t.Fatalf("merge commit has %d parents: %q", len(fields)-1, parents.Stdout)
	}
	if fields[2] != branchHead {
		t.Fatalf("second parent = %s, want the branch head %s", fields[2], branchHead)
	}
}

// A conflict is reported with its paths and the target is left byte-for-byte where
// it was. No conflict markers, no MERGE_HEAD, no moved branch.
func TestAdoptRefusesConflictsAndLeavesTheTargetUntouched(t *testing.T) {
	runner := NewRunner()
	ctx := context.Background()
	_, source, target := initRepoWithTwoWorktrees(t, runner, ctx)

	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("branch version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitAll(t, runner, ctx, source, "branch edits README")
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("trunk version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trunkHead := commitAll(t, runner, ctx, target, "trunk edits README")

	_, err := runner.Adopt(ctx, source, target, "adopt: conflicted")
	if err == nil {
		t.Fatal("adopt of conflicting branches succeeded")
	}
	var conflict *MergeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *MergeConflictError", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "README.md" {
		t.Fatalf("conflict files = %v", conflict.Files)
	}
	if conflict.Stage != "commits" {
		t.Fatalf("conflict stage = %q", conflict.Stage)
	}

	head, herr := runner.Head(ctx, target)
	if herr != nil {
		t.Fatal(herr)
	}
	if head != trunkHead {
		t.Fatalf("target head = %s, want the untouched %s", head, trunkHead)
	}
	assertFile(t, filepath.Join(target, "README.md"), "trunk version\n")
	assertClean(t, runner, ctx, target)
	if _, merr := runner.Run(ctx, target, "rev-parse", "-q", "--verify", "MERGE_HEAD"); merr == nil {
		t.Fatal("target is still mid-merge after a refused adopt")
	}
	// Re-running the adopt must produce the same refusal, not a different state.
	if _, err := runner.Adopt(ctx, source, target, "adopt: conflicted again"); err == nil {
		t.Fatal("repeated adopt of conflicting branches succeeded")
	}
}

// Commits first, then the remaining uncommitted work: both halves must be visible,
// and the result must say which is which.
func TestAdoptMergesCommitsThenAppliesTheDirtyRemainder(t *testing.T) {
	runner := NewRunner()
	ctx := context.Background()
	_, source, target := initRepoWithTwoWorktrees(t, runner, ctx)

	if err := os.WriteFile(filepath.Join(source, "committed.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchHead := commitAll(t, runner, ctx, source, "committed half")
	if err := os.WriteFile(filepath.Join(source, "still-dirty.txt"), []byte("not committed yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runner.Adopt(ctx, source, target, "adopt: both halves")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if result.Mode != AdoptModeFastForward+"+"+AdoptModeWorkingTree {
		t.Fatalf("mode = %q, want fast-forward+working-tree", result.Mode)
	}
	if len(result.Commits) != 1 || result.Commits[0] != branchHead {
		t.Fatalf("commits = %v", result.Commits)
	}
	if result.BranchCommit == "" {
		t.Fatal("the uncommitted half was not captured on the branch")
	}
	assertFile(t, filepath.Join(target, "committed.txt"), "committed\n")
	assertFile(t, filepath.Join(target, "still-dirty.txt"), "not committed yet\n")

	// The committed half is history; the loose half is a working-tree change the
	// user still has to commit (the old adopt semantics, preserved on purpose).
	head, herr := runner.Head(ctx, target)
	if herr != nil {
		t.Fatal(herr)
	}
	if head != branchHead {
		t.Fatalf("target head = %s, want %s", head, branchHead)
	}
	status, serr := runner.Status(ctx, target)
	if serr != nil {
		t.Fatal(serr)
	}
	foundDirty := false
	for _, change := range status.Changes {
		if change.Path == "still-dirty.txt" {
			foundDirty = true
		}
		if change.Path == "committed.txt" {
			t.Fatalf("the committed file is dirty in the target: %+v", status.Changes)
		}
	}
	if !foundDirty {
		t.Fatalf("the remaining dirty file did not land as uncommitted work: %+v", status.Changes)
	}
	// The branch itself keeps a clean record of what it handed over.
	assertClean(t, runner, ctx, source)
}

func commitAll(t *testing.T, r *Runner, ctx context.Context, dir, message string) string {
	t.Helper()
	if err := r.Stage(ctx, dir, nil); err != nil {
		t.Fatalf("stage %s: %v", dir, err)
	}
	sha, err := r.Commit(ctx, dir, contracts.CommitRequest{Message: message})
	if err != nil {
		t.Fatalf("commit %s: %v", dir, err)
	}
	return sha
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// core.autocrlf on Windows rewrites LF to CRLF on checkout; the content, not the
	// line ending, is what this test is about.
	got := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertClean(t *testing.T, r *Runner, ctx context.Context, dir string) {
	t.Helper()
	status, err := r.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status %s: %v", dir, err)
	}
	if !status.Clean {
		t.Fatalf("%s is dirty: changes=%+v conflicts=%v", dir, status.Changes, status.Conflicts)
	}
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
