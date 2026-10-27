package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func newClient(t *testing.T) *Runner {
	t.Helper()
	return New(Config{Timeout: 90 * time.Second})
}

// newRepo creates an empty repository on branch main with one commit holding
// one file.
func newRepo(t *testing.T) (*Runner, string) {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	cl := newClient(t)
	ctx := context.Background()
	if err := cl.Init(ctx, dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	write(t, dir, "a.txt", "hello\n")
	if err := cl.Stage(ctx, dir, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: "first"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return cl, dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestStatus(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	st, err := cl.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if !st.Clean || len(st.Changes) != 0 {
		t.Errorf("expected clean tree, got %+v", st)
	}

	write(t, dir, "a.txt", "hello world\n")
	write(t, dir, "new.txt", "untracked\n")

	st, err = cl.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Clean {
		t.Fatal("expected dirty tree")
	}
	byPath := map[string]contracts.GitChange{}
	for _, c := range st.Changes {
		byPath[c.Path] = c
	}
	if c, ok := byPath["a.txt"]; !ok || c.Worktree != "M" {
		t.Errorf("a.txt change = %+v, want worktree M", c)
	}
	if c, ok := byPath["new.txt"]; !ok || c.Worktree != "?" {
		t.Errorf("new.txt change = %+v, want untracked", c)
	}

	if err := cl.Stage(ctx, dir, []string{"a.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	st, err = cl.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, c := range st.Changes {
		if c.Path == "a.txt" && c.Index != "M" {
			t.Errorf("a.txt index = %q, want M", c.Index)
		}
	}

	if err := cl.Unstage(ctx, dir, []string{"a.txt"}); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	st, err = cl.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, c := range st.Changes {
		if c.Path == "a.txt" && (c.Index != "." || c.Worktree != "M") {
			t.Errorf("after unstage a.txt = %+v", c)
		}
	}
}

func TestStatusUnbornBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	cl := newClient(t)
	ctx := context.Background()
	if err := cl.Init(ctx, dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	write(t, dir, "a.txt", "one\n")

	st, err := cl.Status(ctx, dir)
	if err != nil {
		t.Fatalf("status on unborn branch: %v", err)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if len(st.Changes) != 1 {
		t.Errorf("changes = %+v, want one untracked file", st.Changes)
	}

	graph, err := cl.Log(ctx, dir, 10, "")
	if err != nil {
		t.Fatalf("log on unborn branch: %v", err)
	}
	if len(graph.Commits) != 0 {
		t.Errorf("expected no commits, got %d", len(graph.Commits))
	}
}

func TestDiffScopes(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	write(t, dir, "a.txt", "hello\nworld\n")

	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil {
		t.Fatalf("diff unstaged: %v", err)
	}
	if len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("unstaged files = %+v", files)
	}
	if files[0].Additions != 1 || files[0].Deletions != 0 {
		t.Errorf("counts = +%d -%d, want +1 -0", files[0].Additions, files[0].Deletions)
	}
	if !strings.Contains(files[0].Patch, "@@") {
		t.Errorf("patch missing hunk header: %q", files[0].Patch)
	}

	if err := cl.Stage(ctx, dir, []string{"a.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeStaged})
	if err != nil {
		t.Fatalf("diff staged: %v", err)
	}
	if len(staged) != 1 || staged[0].Additions != 1 {
		t.Fatalf("staged = %+v", staged)
	}
	unstaged, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil {
		t.Fatalf("diff unstaged after stage: %v", err)
	}
	if len(unstaged) != 0 {
		t.Errorf("unstaged after staging = %+v, want none", unstaged)
	}

	head, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: "second"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	byCommit, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeCommit, Commit: head})
	if err != nil {
		t.Fatalf("diff commit: %v", err)
	}
	if len(byCommit) != 1 || byCommit[0].Additions != 1 {
		t.Fatalf("commit diff = %+v", byCommit)
	}

	// Path filter that matches nothing stays empty rather than erroring.
	none, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeCommit, Commit: head, Path: "b.txt"})
	if err != nil {
		t.Fatalf("diff with path filter: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("filtered diff = %+v, want none", none)
	}

	// Branch scope with an explicit base.
	if err := cl.CreateAndCheckout(ctx, dir, "feature"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	write(t, dir, "b.txt", "feature\n")
	if err := cl.Stage(ctx, dir, []string{"b.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: "feature work"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	branchDiff, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeBranch, Base: "main"})
	if err != nil {
		t.Fatalf("diff branch: %v", err)
	}
	if len(branchDiff) != 1 || branchDiff[0].Path != "b.txt" || branchDiff[0].Status != "added" {
		t.Errorf("branch diff = %+v", branchDiff)
	}
}

func TestDiffRenameAndBinary(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "renamed.txt")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	// Rename detection and binary patches only show up once the new paths are
	// in the index (git diff alone cannot see untracked files).
	if err := cl.Stage(ctx, dir, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	files, err := cl.Diff(ctx, dir, contracts.DiffRequest{Scope: contracts.ScopeStaged})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	var renamed, binary *contracts.FileDiff
	for i := range files {
		switch files[i].Path {
		case "renamed.txt":
			renamed = &files[i]
		case "blob.bin":
			binary = &files[i]
		}
	}
	if renamed == nil {
		t.Fatalf("missing rename in %+v", files)
	}
	if renamed.Status != "renamed" || renamed.OldPath != "a.txt" {
		t.Errorf("rename = %+v", renamed)
	}
	if binary == nil || !binary.Binary {
		t.Fatalf("binary file not detected: %+v", files)
	}
}

func TestLogPaginationAndRefs(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()
	for _, name := range []string{"two", "three"} {
		write(t, dir, name+".txt", name+"\n")
		if err := cl.Stage(ctx, dir, nil); err != nil {
			t.Fatalf("stage: %v", err)
		}
		if _, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: name}); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}
	if _, err := cl.Run(ctx, dir, "tag", "v1.0"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	page, err := cl.Log(ctx, dir, 2, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(page.Commits) != 2 {
		t.Fatalf("page 1 = %d commits, want 2", len(page.Commits))
	}
	if page.NextCursor != "2" {
		t.Errorf("next cursor = %q, want 2", page.NextCursor)
	}
	if page.Head == "" || page.Head != page.Commits[0].Hash {
		t.Errorf("head = %q, first = %q", page.Head, page.Commits[0].Hash)
	}
	if page.Commits[0].Subject != "three" || page.Commits[0].Short == "" {
		t.Errorf("first commit = %+v", page.Commits[0])
	}
	if len(page.Commits[0].Parents) != 1 {
		t.Errorf("parents = %v, want 1", page.Commits[0].Parents)
	}
	// The tag points at HEAD, i.e. the newest commit ("three").
	kinds := map[string]string{}
	for _, ref := range page.Commits[0].Refs {
		kinds[ref.Type] = ref.Name
	}
	if kinds["tag"] != "v1.0" || kinds["head"] != "HEAD" || kinds["branch"] != "main" {
		t.Errorf("newest commit refs = %+v, want head+branch+tag", page.Commits[0].Refs)
	}

	page2, err := cl.Log(ctx, dir, 2, page.NextCursor)
	if err != nil {
		t.Fatalf("log page 2: %v", err)
	}
	if len(page2.Commits) != 1 {
		t.Fatalf("page 2 = %d commits, want 1", len(page2.Commits))
	}
	if page2.NextCursor != "" {
		t.Errorf("page 2 cursor = %q, want empty", page2.NextCursor)
	}

	refs := parseRefs("HEAD -> main, tag: v1.0, origin/main, side")
	if len(refs) != 5 {
		t.Fatalf("refs = %+v, want 5", refs)
	}
	if refs[0].Type != "head" || refs[1].Type != "branch" || refs[2].Type != "tag" || refs[3].Type != "remote" || refs[4].Type != "branch" {
		t.Errorf("ref kinds = %+v", refs)
	}
}

func TestCommitAmendAndCheckout(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()

	write(t, dir, "b.txt", "b\n")
	head, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: "second", Paths: []string{"b.txt"}})
	if err != nil {
		t.Fatalf("commit with paths: %v", err)
	}
	amended, err := cl.Commit(ctx, dir, contracts.CommitRequest{Message: "second (amended)", Amend: true})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if amended == head {
		t.Errorf("amend did not rewrite the commit")
	}
	graph, err := cl.Log(ctx, dir, 10, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(graph.Commits) != 2 {
		t.Fatalf("commits after amend = %d, want 2", len(graph.Commits))
	}
	if graph.Commits[0].Subject != "second (amended)" {
		t.Errorf("subject = %q", graph.Commits[0].Subject)
	}

	if err := cl.CreateBranch(ctx, dir, "feature"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := cl.Checkout(ctx, dir, "feature"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	branch, err := cl.CurrentBranch(ctx, dir)
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}
	if branch != "feature" {
		t.Errorf("branch = %q, want feature", branch)
	}
	if err := cl.Checkout(ctx, dir, "does-not-exist"); err == nil {
		t.Error("checkout of a missing branch should fail")
	}
	if err := cl.CreateBranch(ctx, dir, "bad name"); err == nil {
		t.Error("create branch with a space should fail")
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt-feature")

	if err := cl.AddWorktree(ctx, dir, wt, "feature", true); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(wt, "a.txt")); err != nil || fi.IsDir() {
		t.Fatalf("worktree checkout missing a.txt: %v", err)
	}
	list, err := cl.ListWorktrees(ctx, dir)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("worktrees = %+v, want 2", list)
	}

	if err := cl.RemoveWorktree(ctx, dir, wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still present: %v", err)
	}
	list, err = cl.ListWorktrees(ctx, dir)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("worktrees after remove = %+v, want 1", list)
	}
	// Pruning an already clean repository is a no-op.
	if err := cl.PruneWorktrees(ctx, dir); err != nil {
		t.Errorf("prune: %v", err)
	}
	// A worktree inside the repository is refused.
	if err := cl.AddWorktree(ctx, dir, filepath.Join(dir, "nested"), "nested", true); err == nil {
		t.Error("nested worktree should be refused")
	}
}

// TestWorktreeIsAWorkspaceRoot is the shape the server actually uses: the HTTP
// layer hands Workspace an absolute path that is a *linked worktree* (its .git
// is a file pointing at the parent repository), not a primary checkout.
func TestWorktreeIsAWorkspaceRoot(t *testing.T) {
	cl, dir := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "linked")

	if err := cl.AddWorktree(ctx, dir, wt, "session/one", true); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// A worktree has a .git FILE, which is what breaks naive tooling.
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf(".git should be a file in a linked worktree: %v %v", fi, err)
	}

	st, err := cl.Status(ctx, wt)
	if err != nil {
		t.Fatalf("status in worktree: %v", err)
	}
	if st.Branch != "session/one" || !st.Clean {
		t.Fatalf("status = %+v", st)
	}

	write(t, wt, "a.txt", "changed in the worktree\n")
	files, err := cl.Diff(ctx, wt, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil || len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("diff in worktree = %+v, %v", files, err)
	}

	if err := cl.Stage(ctx, wt, []string{"a.txt"}); err != nil {
		t.Fatalf("stage in worktree: %v", err)
	}
	staged, err := cl.Diff(ctx, wt, contracts.DiffRequest{Scope: contracts.ScopeStaged})
	if err != nil || len(staged) != 1 {
		t.Fatalf("staged diff in worktree = %+v, %v", staged, err)
	}
	if _, err := cl.Commit(ctx, wt, contracts.CommitRequest{Message: "worktree commit"}); err != nil {
		t.Fatalf("commit in worktree: %v", err)
	}
	graph, err := cl.Log(ctx, wt, 10, "")
	if err != nil {
		t.Fatalf("log in worktree: %v", err)
	}
	if len(graph.Commits) != 2 || graph.Commits[0].Subject != "worktree commit" {
		t.Fatalf("log = %+v", graph)
	}
	// The commit is visible from the parent checkout too (shared object store).
	parent, err := cl.Log(ctx, dir, 10, "")
	if err != nil || len(parent.Commits) != 2 {
		t.Fatalf("parent log = %+v, %v", parent, err)
	}

	if err := cl.RemoveWorktree(ctx, dir, wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still present")
	}
	// The branch survives the worktree removal and can still be resolved.
	if ok, err := cl.BranchExists(ctx, dir, "session/one"); err != nil || !ok {
		t.Errorf("branch after remove = %v, %v", ok, err)
	}
}

func TestParsePorcelainV2(t *testing.T) {
	out := strings.Join([]string{
		"# branch.oid abc123def456",
		"# branch.head feature",
		"# branch.upstream origin/feature",
		"# branch.ab +2 -3",
		"1 .M N... 100644 100644 100644 aaa bbb src/main.go",
		"1 M. N... 100644 100644 100644 ccc ddd src/other.go",
		"2 R. N... 100644 100644 100644 eee fff R100 new/name.go\told/name.go",
		"? notes.txt",
		"u UU N... 100644 100644 100644 100644 111 222 333 conflict.txt",
	}, "\n") + "\n"

	st, err := parsePorcelainV2(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Branch != "feature" || st.Ahead != 2 || st.Behind != 3 {
		t.Errorf("branch/ab = %+v", st)
	}
	if st.Clean {
		t.Error("expected dirty")
	}
	if len(st.Conflicts) != 1 || st.Conflicts[0] != "conflict.txt" {
		t.Errorf("conflicts = %+v", st.Conflicts)
	}
	byPath := map[string]contracts.GitChange{}
	for _, c := range st.Changes {
		byPath[c.Path] = c
	}
	if c := byPath["src/other.go"]; c.Index != "M" || c.Worktree != "." {
		t.Errorf("src/other.go = %+v", c)
	}
	if c := byPath["new/name.go"]; c.Index != "R" {
		t.Errorf("rename entry = %+v", c)
	}
	if c := byPath["notes.txt"]; c.Worktree != "?" {
		t.Errorf("untracked = %+v", c)
	}
}

func TestParsePatch(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/added.txt b/added.txt",
		"new file mode 100644",
		"index 0000000..1111111",
		"--- /dev/null",
		"+++ b/added.txt",
		"@@ -0,0 +1,2 @@",
		"+one",
		"+two",
		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"index 1111111..0000000",
		"--- a/gone.txt",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-bye",
		"-now",
		"diff --git a/old.txt b/new.txt",
		"similarity index 92%",
		"rename from old.txt",
		"rename to new.txt",
		"diff --git a/img.png b/img.png",
		"index 1111111..2222222 100644",
		"Binary files a/img.png and b/img.png differ",
	}, "\n") + "\n"

	files := ParsePatch(patch)
	if len(files) != 4 {
		t.Fatalf("files = %d, want 4: %+v", len(files), files)
	}
	if files[0].Status != "added" || files[0].Additions != 2 || files[0].Deletions != 0 {
		t.Errorf("added = %+v", files[0])
	}
	if files[1].Status != "deleted" || files[1].Deletions != 2 || files[1].Path != "gone.txt" {
		t.Errorf("deleted = %+v", files[1])
	}
	if files[2].Status != "renamed" || files[2].OldPath != "old.txt" || files[2].Path != "new.txt" {
		t.Errorf("renamed = %+v", files[2])
	}
	if !files[3].Binary || files[3].Path != "img.png" {
		t.Errorf("binary = %+v", files[3])
	}
}

func TestPathGuards(t *testing.T) {
	root := t.TempDir()
	if p, err := SecurePath(root, "sub/file.go"); err != nil || !strings.HasPrefix(p, root) {
		t.Errorf("SecurePath ok-case = %q, %v", p, err)
	}
	for _, bad := range []string{"../../etc/passwd", "..", "sub/../../escape", "C:\\Windows\\win.ini", "/etc/passwd", "a\x00b"} {
		if _, err := SecurePath(root, bad); err == nil {
			t.Errorf("SecurePath(%q) should fail", bad)
		}
	}
	if _, err := CleanRel("a/./b/../c"); err != nil {
		t.Errorf("CleanRel relative = %v", err)
	}
	if got, _ := CleanRel("a/./b/../c"); got != "a/c" && got != strings.Join([]string{"a", "c"}, string(filepath.Separator)) {
		t.Errorf("CleanRel = %q", got)
	}
	if _, err := SecureAbsPath(root, filepath.Join(root, "x")); err != nil {
		t.Errorf("SecureAbsPath inside = %v", err)
	}
	if _, err := SecureAbsPath(root, filepath.Dir(root)); err == nil {
		t.Error("SecureAbsPath outside should fail")
	}
}
