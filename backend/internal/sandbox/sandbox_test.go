package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func newLocal(t *testing.T) *Local {
	t.Helper()
	l, err := NewLocal(LocalConfig{Root: filepath.Join(t.TempDir(), "sandboxes")})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return l
}

// echoArgv returns a portable argv that prints "piforge-ok".
func echoArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "echo piforge-ok"}
	}
	return []string{"echo", "piforge-ok"}
}

// sleepArgv returns a portable argv that sleeps for a few seconds.
func sleepArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 5"}
	}
	return []string{"sleep", "5"}
}

func TestNewLocalValidation(t *testing.T) {
	if _, err := NewLocal(LocalConfig{}); err == nil {
		t.Fatal("empty root should fail")
	}
	l := newLocal(t)
	if l.Root() == "" {
		t.Fatal("root not recorded")
	}
}

func TestEnsureInitIsIdempotent(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()

	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if sb.State != contracts.SandboxReady {
		t.Errorf("state = %q, want READY", sb.State)
	}
	if sb.ID != ID("user-1", "proj-1") {
		t.Errorf("id = %q, want deterministic %q", sb.ID, ID("user-1", "proj-1"))
	}
	if sb.Provider != ProviderName || sb.Image == "" {
		t.Errorf("provider/image = %q/%q", sb.Provider, sb.Image)
	}
	wantDir := filepath.Join(l.Root(), "user-1", "proj-1")
	if sb.RootDir != wantDir {
		t.Errorf("root dir = %q, want %q", sb.RootDir, wantDir)
	}
	if fi, err := os.Stat(wantDir); err != nil || !fi.IsDir() {
		t.Fatalf("sandbox dir missing: %v", err)
	}
	// A bare git init is not enough for worktrees: Ensure must leave a commit.
	initGit := gitx.New(gitx.Config{})
	repoDir := filepath.Join(wantDir, repoSubdir)
	if fi, err := os.Stat(repoDir); err != nil || !fi.IsDir() {
		t.Fatalf("sandbox repo dir missing: %v", err)
	}
	if has, err := initGit.HasCommits(ctx, repoDir); err != nil || !has {
		t.Fatalf("initial commit missing: %v %v", has, err)
	}
	branch, err := initGit.CurrentBranch(ctx, repoDir)
	if err != nil || branch != DefaultInitialBranch {
		t.Errorf("branch = %q, want %q", branch, DefaultInitialBranch)
	}

	// Second call re-adopts the same sandbox.
	again, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure twice: %v", err)
	}
	if again.ID != sb.ID {
		t.Errorf("id changed: %q vs %q", again.ID, sb.ID)
	}
	if !again.CreatedAt.Equal(sb.CreatedAt) {
		t.Errorf("created_at changed: %v vs %v", again.CreatedAt, sb.CreatedAt)
	}
	got, err := l.Get(ctx, sb.ID)
	if err != nil || got.ID != sb.ID || got.RootDir != wantDir {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A fresh provider (server restart) finds the same sandbox on disk.
	l2, err := NewLocal(LocalConfig{Root: l.Root()})
	if err != nil {
		t.Fatalf("NewLocal again: %v", err)
	}
	reloaded, err := l2.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if reloaded.RootDir != wantDir || reloaded.State != contracts.SandboxReady {
		t.Errorf("reloaded = %+v", reloaded)
	}
	if _, err := l2.Get(ctx, "sbx_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
}

func TestEnsureCreatesBranch(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "feature/x")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	branch, err := gitx.New(gitx.Config{}).CurrentBranch(ctx, filepath.Join(sb.RootDir, repoSubdir))
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", branch)
	}
	// Calling Ensure again with the same branch must not fail.
	if _, err := l.Ensure(ctx, "user-1", "proj-1", "", "feature/x"); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
}

func TestEnsureClonesSourceRepo(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()

	src := t.TempDir()
	git := gitx.New(gitx.Config{})
	if err := git.Init(ctx, src, "main"); err != nil {
		t.Fatalf("init src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := git.Stage(ctx, src, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := git.Commit(ctx, src, contracts.CommitRequest{Message: "init"}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	sb, err := l.Ensure(ctx, "user-1", "proj-1", src, "")
	if err != nil {
		t.Fatalf("ensure clone: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(sb.RootDir, "repo", "README.md")); err != nil || strings.TrimSpace(string(body)) != "hello" {
		t.Fatalf("clone missing file: %q %v", body, err)
	}
}

func TestWorktreeInsideSandbox(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	git := gitx.New(gitx.Config{})
	repoDir, err := l.RepoDir(sb.ID)
	if err != nil {
		t.Fatalf("repo dir: %v", err)
	}
	wt, err := l.WorktreePath(sb.ID, "fix-cache")
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	if err := git.AddWorktree(ctx, repoDir, wt, "fix-cache", true); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// This is the exact server shape: status/diff/log against the worktree.
	st, err := git.Status(ctx, wt)
	if err != nil {
		t.Fatalf("status in worktree: %v", err)
	}
	if st.Branch != "fix-cache" || !st.Clean {
		t.Errorf("worktree status = %+v", st)
	}
	if err := os.WriteFile(filepath.Join(wt, "change.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	files, err := git.Diff(ctx, wt, contracts.DiffRequest{Scope: contracts.ScopeUnstaged})
	if err != nil {
		t.Fatalf("diff in worktree: %v", err)
	}
	_ = files // untracked files do not appear in an unstaged diff
	if err := git.Stage(ctx, wt, nil); err != nil {
		t.Fatalf("stage in worktree: %v", err)
	}
	files, err = git.Diff(ctx, wt, contracts.DiffRequest{Scope: contracts.ScopeStaged})
	if err != nil || len(files) != 1 || files[0].Path != "change.txt" {
		t.Fatalf("staged diff = %+v, %v", files, err)
	}
	if _, err := git.Commit(ctx, wt, contracts.CommitRequest{Message: "from worktree"}); err != nil {
		t.Fatalf("commit in worktree: %v", err)
	}
	graph, err := git.Log(ctx, wt, 5, "")
	if err != nil || len(graph.Commits) < 2 {
		t.Fatalf("log in worktree = %+v, %v", graph, err)
	}
	// Removing the worktree must also prune the parent's bookkeeping.
	if err := git.RemoveWorktree(ctx, repoDir, wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	list, err := git.ListWorktrees(ctx, repoDir)
	if err != nil || len(list) != 1 {
		t.Errorf("worktrees after remove = %+v, %v", list, err)
	}
}

func TestExec(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	out, err := l.Exec(ctx, sb.ID, echoArgv(), "")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "piforge-ok") {
		t.Errorf("output = %q", out)
	}
	// cwd defaults to the sandbox root and relative dirs stay inside it.
	if err := os.MkdirAll(filepath.Join(sb.RootDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := l.Exec(ctx, sb.ID, echoArgv(), "sub"); err != nil {
		t.Errorf("exec in sub: %v", err)
	}
	if _, err := l.Exec(ctx, sb.ID, echoArgv(), "../escape"); err == nil {
		t.Errorf("exec outside the sandbox should fail")
	}
	if _, err := l.Exec(ctx, sb.ID, nil, ""); err == nil {
		t.Errorf("empty argv should fail")
	}
	if _, err := l.Exec(ctx, "sbx_nope", echoArgv(), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox err = %v", err)
	}
	// A failing command returns its combined output plus an error.
	if _, err := l.Exec(ctx, sb.ID, []string{"definitely-not-a-binary-42"}, ""); err == nil {
		t.Errorf("missing binary should fail")
	}
}

func TestExecCancellation(t *testing.T) {
	requireGit(t)
	if _, err := exec.LookPath(sleepArgv()[0]); err != nil {
		t.Skipf("%s not available", sleepArgv()[0])
	}
	l := newLocal(t)
	sb, err := l.Ensure(context.Background(), "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = l.Exec(ctx, sb.ID, sleepArgv(), "")
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
}

func TestPauseResume(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := l.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, err := l.Get(ctx, sb.ID)
	if err != nil || got.State != contracts.SandboxPaused {
		t.Fatalf("paused sandbox = %+v, %v", got, err)
	}
	if _, err := l.Exec(ctx, sb.ID, echoArgv(), ""); !errors.Is(err, ErrPaused) {
		t.Errorf("exec on paused = %v", err)
	}
	resumed, err := l.Resume(ctx, sb.ID)
	if err != nil || resumed.State != contracts.SandboxReady {
		t.Fatalf("resume = %+v, %v", resumed, err)
	}
	if _, err := l.Exec(ctx, sb.ID, echoArgv(), ""); err != nil {
		t.Errorf("exec after resume: %v", err)
	}
	// State survives a restart.
	l2, err := NewLocal(LocalConfig{Root: l.Root()})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := l2.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("pause after reload: %v", err)
	}
	if got, _ := l2.Get(ctx, sb.ID); got.State != contracts.SandboxPaused {
		t.Errorf("reloaded state = %+v", got)
	}
}

func TestDestroy(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := l.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(sb.RootDir); !os.IsNotExist(err) {
		t.Errorf("sandbox dir still present: %v", err)
	}
	if _, err := l.Get(ctx, sb.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after destroy = %v", err)
	}
	// Nothing outside the root is ever deleted.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	l.mu.Lock()
	l.known["sbx_evil"] = &contracts.Sandbox{ID: "sbx_evil", UserID: "u", ProjectID: "p", RootDir: outside, State: contracts.SandboxReady}
	l.mu.Unlock()
	if err := l.Destroy(ctx, "sbx_evil"); !errors.Is(err, ErrDestroyRefused) {
		t.Fatalf("destroy outside err = %v, want ErrDestroyRefused", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Errorf("outside data was deleted: %v", err)
	}
}

func TestInvalidIdentifiers(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()
	bad := []string{"", "..", "../x", "a/b", `a\b`, "a:b", "a*b", strings.Repeat("x", 200)}
	for _, id := range bad {
		if _, err := l.Ensure(ctx, id, "proj", "", ""); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Ensure user %q err = %v, want ErrInvalidID", id, err)
		}
		if _, err := l.Ensure(ctx, "user", id, "", ""); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Ensure project %q err = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestStateFileLivesOutsideSandbox(t *testing.T) {
	requireGit(t)
	l := newLocal(t)
	ctx := context.Background()
	sb, err := l.Ensure(ctx, "user-1", "proj-1", "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// The state file must not pollute the git checkout.
	entries, err := os.ReadDir(sb.RootDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("state file leaked into the sandbox: %s", e.Name())
		}
	}
	if _, err := os.Stat(l.statePath("user-1", "proj-1")); err != nil {
		t.Errorf("state file missing: %v", err)
	}
	// A vanished directory is reported as MISSING rather than recreated.
	if err := os.RemoveAll(sb.RootDir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	l.forget(sb.ID)
	got, err := l.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got.State != contracts.SandboxMissing {
		t.Errorf("state = %q, want MISSING", got.State)
	}
}
