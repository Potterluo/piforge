package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// A remote against a local bare repository: real git, real refs, no network. That is
// the same thing a session does over HTTPS, minus the transport.
func remoteFixture(t *testing.T) (bare, seed string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	bare = filepath.Join(root, "origin.git")
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"), "GIT_CONFIG_SYSTEM=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(root, "init", "--bare", "-b", "main", bare)

	seed = filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run(seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "seed")
	run(seed, "remote", "add", "origin", bare)
	run(seed, "push", "-u", "origin", "main")
	return bare, seed
}

// FR-8.7: a commit made in the workbench can leave the machine, and a remote change can
// come back — without dropping to a terminal.
func TestPushAndPullRoundTrip(t *testing.T) {
	bare, seed := remoteFixture(t)

	clone := filepath.Join(t.TempDir(), "work")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if out, err := exec.Command("git", "clone", bare, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	runner := NewRunner()
	ctx := context.Background()

	// Nothing to push yet.
	ahead, behind, err := runner.AheadBehind(ctx, clone)
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	if ahead != 0 || behind != 0 {
		t.Fatalf("a fresh clone is %d ahead, %d behind", ahead, behind)
	}

	if err := os.WriteFile(filepath.Join(clone, "work.txt"), []byte("from the workbench\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := runner.Stage(ctx, clone, nil); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := runner.Commit(ctx, clone, contracts.CommitRequest{Message: "a change"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	ahead, behind, err = runner.AheadBehind(ctx, clone)
	if err != nil {
		t.Fatalf("AheadBehind after commit: %v", err)
	}
	if ahead != 1 || behind != 0 {
		t.Fatalf("after one commit: ahead=%d behind=%d", ahead, behind)
	}

	result, err := runner.Push(ctx, clone)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if result.Branch != "main" || result.Commit == "" {
		t.Fatalf("push result = %+v", result)
	}
	// The commit really is on the remote.
	if out, err := exec.Command("git", "--git-dir", bare, "log", "--format=%s", "main").CombinedOutput(); err != nil {
		t.Fatalf("log on the bare repo: %v\n%s", err, out)
	} else if !strings.Contains(string(out), "a change") {
		t.Fatalf("the remote does not have the commit:\n%s", out)
	}

	// Somebody else pushes; a pull brings it down. They have to catch up first — a
	// behind branch is rejected by the remote, which is the same rule the workbench
	// enforces on the user (it would be strange if the test ignored it).
	if err := os.WriteFile(filepath.Join(seed, "upstream.txt"), []byte("from upstream\n"), 0o644); err != nil {
		t.Fatalf("write upstream: %v", err)
	}
	for _, args := range [][]string{{"pull", "--ff-only", "origin", "main"}, {"add", "-A"}, {"commit", "-m", "upstream"}, {"push", "origin", "main"}} {
		cmd := exec.Command("git", append([]string{"-C", seed}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	pulled, err := runner.Pull(ctx, clone)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !pulled.Changed {
		t.Fatal("the pull reported no change after an upstream commit")
	}
	if _, err := os.Stat(filepath.Join(clone, "upstream.txt")); err != nil {
		t.Fatalf("the pulled file is missing: %v", err)
	}
}

// A checkout without a remote must say so, not fail with a raw git error.
func TestPushWithoutRemoteSaysSo(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner()
	ctx := context.Background()
	if err := runner.Init(ctx, dir, "main"); err != nil {
		t.Skipf("git not usable here: %v", err)
	}
	if _, err := runner.Push(ctx, dir); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("push without a remote = %v", err)
	}
	if _, err := runner.Pull(ctx, dir); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("pull without a remote = %v", err)
	}
	if err := runner.Fetch(ctx, dir); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("fetch without a remote = %v", err)
	}
}
