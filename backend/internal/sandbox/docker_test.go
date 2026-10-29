package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The Docker provider is a spike with a real test: it runs when a daemon and an image are
// available, and skips loudly otherwise. A test that silently passes without Docker would
// be worse than no test.
func dockerTestImage() string {
	if image := os.Getenv("PIFORGE_TEST_SANDBOX_IMAGE"); image != "" {
		return image
	}
	return "alpine:3.20"
}

func newDockerForTest(t *testing.T) *Docker {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("the docker daemon is not reachable")
	}
	provider, err := NewDocker(DockerConfig{
		Image:    dockerTestImage(),
		DataRoot: t.TempDir(),
		CPUs:     "1",
		Memory:   "256m",
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	return provider
}

// requireCheckoutImage skips when the configured image cannot host the checkout
// step. Ensure now runs git inside the container, so an image without git would
// exercise a failure path instead of the provider; alpine:3.20, the default test
// image, has neither git nor a sandbox user. Set PIFORGE_TEST_SANDBOX_IMAGE to the
// sandbox image (the AIO image ships git and creates `gem` at startup) to run
// these tests.
//
// The probe is thrown away by --rm, so skipping leaves nothing behind in
// `docker ps -a`.
func requireCheckoutImage(t *testing.T, provider *Docker) {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh",
		provider.cfg.Image, "-c", "command -v git").CombinedOutput()
	if err != nil {
		t.Skipf("image %s cannot host the provider's checkout (no git): %v: %s; "+
			"set PIFORGE_TEST_SANDBOX_IMAGE to the sandbox image",
			provider.cfg.Image, err, strings.TrimSpace(string(out)))
	}
}

// execAsGem runs argv in the container as the sandbox user — the account the
// panel's git and the sandbox shell use. Running the assertions as root would
// hide exactly the ownership bug this provider has to avoid.
func execAsGem(t *testing.T, sandboxID string, argv ...string) (string, error) {
	t.Helper()
	args := append([]string{"exec", "-u", "gem", sandboxID}, argv...)
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// The lifecycle the store and the UI depend on: create, exec, pause, resume, destroy.
func TestDockerProviderLifecycle(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_test_docker", "prj_test_docker"

	created, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Skipf("cannot start a container here: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })

	if created.State != contracts.SandboxReady {
		t.Fatalf("state after create = %q", created.State)
	}
	if created.ID != provider.containerName(userID, projectID) {
		t.Fatalf("id = %q, want a name derived from the pair", created.ID)
	}

	// The same pair returns the same sandbox, not a second one.
	again, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure twice: %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("a second Ensure made a second container: %q", again.ID)
	}

	out, err := provider.Exec(ctx, created.ID, []string{"sh", "-c", "echo sandbox-ok"}, "")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "sandbox-ok" {
		t.Fatalf("Exec output = %q", out)
	}

	if err := provider.Pause(ctx, created.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused, err := provider.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after pause: %v", err)
	}
	if paused.State != contracts.SandboxPaused {
		t.Fatalf("state after pause = %q", paused.State)
	}

	resumed, err := provider.Resume(ctx, created.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != contracts.SandboxReady {
		t.Fatalf("state after resume = %q", resumed.State)
	}

	if err := provider.Destroy(ctx, created.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := provider.Get(ctx, created.ID); err == nil {
		t.Fatal("a destroyed container still reports a state")
	}
}

// A name the provider does not know is ErrNotFound, not a raw docker error: the store
// uses that distinction to decide whether to rebuild.
func TestDockerProviderUnknownSandbox(t *testing.T) {
	provider := newDockerForTest(t)
	ctx := context.Background()
	if _, err := provider.Get(ctx, "piforge-doesnotexist"); err == nil {
		t.Fatal("unknown sandbox returned no error")
	}
}

// The control plane reaches the sandbox through a published port, so the provider has to
// report the one Docker actually assigned — not the one it asked for.
func TestDockerProviderPublishesTheAPIPort(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	provider.cfg.APIPort = 8080
	ctx := context.Background()

	created, err := provider.Ensure(ctx, "usr_port", "prj_port", "", "main")
	if err != nil {
		t.Skipf("cannot start a container here: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })

	endpoint, err := provider.Endpoint(ctx, created.ID)
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if strings.HasSuffix(endpoint, ":8080") {
		t.Logf("the published port happens to be 8080; that is fine, it is still read back")
	}
}

// The terminal has a host worktree path and needs the path the container sees.
func TestContainerPath(t *testing.T) {
	root := t.TempDir()
	provider := &Docker{cfg: DockerConfig{DataRoot: root}}

	got, err := provider.ContainerPath(filepath.Join(root, "sbx", "repo", "worktrees", "feat"))
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if got != "/workspace/sbx/repo/worktrees/feat" {
		t.Fatalf("mapped = %q", got)
	}

	// A sandbox record may already carry the container path.
	got, err = provider.ContainerPath("/workspace/repo")
	if err != nil || got != "/workspace/repo" {
		t.Fatalf("already-mapped = %q, %v", got, err)
	}

	if _, err := provider.ContainerPath(t.TempDir()); err == nil {
		t.Fatal("a path outside the mounted workspace was accepted")
	}
}

// The checkout argv is the contract: each case is pinned exactly, because these
// commands run inside the container as `gem` and a silently different list would
// bring back either a missing repository (no commit to branch worktrees from) or a
// root-owned one the sandbox user cannot stage in.
func TestPlanCheckout(t *testing.T) {
	tests := []struct {
		name     string
		ready    bool
		repoURL  string
		branch   string
		want     [][]string
		wantName []string
		fallback []string
	}{
		{
			name:    "existing repo with a commit is left alone",
			ready:   true,
			repoURL: "https://example.invalid/acme/widgets.git",
			branch:  "develop",
		},
		{
			name:   "empty project: init, identity, one empty commit",
			branch: "",
			want: [][]string{
				{"git", "init", "-q", "-b", "main"},
				{"git", "config", "user.name", "PiForge Sandbox"},
				{"git", "config", "user.email", "sandbox@piforge.local"},
				{"git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", "piforge: initial commit"},
			},
			wantName: []string{"init", "init", "init", "commit"},
		},
		{
			name:   "the requested branch is the one initialized",
			branch: " develop ",
			want: [][]string{
				{"git", "init", "-q", "-b", "develop"},
				{"git", "config", "user.name", "PiForge Sandbox"},
				{"git", "config", "user.email", "sandbox@piforge.local"},
				{"git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", "piforge: initial commit"},
			},
			wantName: []string{"init", "init", "init", "commit"},
		},
		{
			name:    "repo with url: clone, then checkout with a create fallback",
			repoURL: " https://example.invalid/acme/widgets.git ",
			branch:  "develop",
			want: [][]string{
				{"git", "clone", "--quiet", "--", "https://example.invalid/acme/widgets.git", "."},
				{"git", "checkout", "-q", "develop"},
			},
			wantName: []string{"clone", "checkout"},
			// Local's checkoutBranch creates a branch that does not exist yet rather
			// than failing; the container does the same on the second attempt.
			fallback: []string{"git", "checkout", "-q", "-b", "develop"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := planCheckout(tt.ready, tt.repoURL, tt.branch)
			var argv [][]string
			var names []string
			var fallbacks [][]string
			for _, step := range steps {
				argv = append(argv, step.argv)
				names = append(names, step.name)
				if step.fallback != nil {
					fallbacks = append(fallbacks, step.fallback)
				}
			}
			if !reflect.DeepEqual(argv, tt.want) {
				t.Fatalf("argv = %q\nwant    %q", argv, tt.want)
			}
			if !reflect.DeepEqual(names, tt.wantName) {
				t.Fatalf("step names = %q, want %q", names, tt.wantName)
			}
			if len(tt.fallback) > 0 {
				if len(fallbacks) != 1 || !reflect.DeepEqual(fallbacks[0], tt.fallback) {
					t.Fatalf("fallbacks = %q, want [%q]", fallbacks, tt.fallback)
				}
			} else if len(fallbacks) != 0 {
				t.Fatalf("unexpected fallbacks: %q", fallbacks)
			}
		})
	}
}

// The defect this pins: with -sandbox docker, Ensure started a container and never
// created /workspace/repo/.git, so every git endpoint answered 500 on a fresh
// project. The second half is ownership: a checkout made by the host (or as root)
// is not writable by the sandbox user, and `git stage` then fails with
// "Unable to create '.git/index.lock': Permission denied".
//
// Every assertion below runs as `gem`, through the same `docker exec -u gem` the
// panel's git effectively uses.
func TestDockerProviderInitialisesTheCheckout(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_checkout", "prj_checkout"

	created, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })
	if created.State != contracts.SandboxReady {
		t.Fatalf("state after create = %q", created.State)
	}

	if out, err := execAsGem(t, created.ID, "sh", "-c", "test -d /workspace/repo/.git && echo yes"); err != nil || out != "yes" {
		t.Fatalf("no .git inside the container: out=%q err=%v", out, err)
	}

	// git status has to work for the shell user, not only for root: "detected
	// dubious ownership" is what a root-owned checkout answers.
	status, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status as gem: %v: %s", err, status)
	}

	branch, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("current branch as gem: %v: %s", err, branch)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	subject, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "log", "-1", "--format=%s")
	if err != nil {
		t.Fatalf("git log as gem: %v: %s", err, subject)
	}
	if subject != "piforge: initial commit" {
		t.Fatalf("HEAD subject = %q, want the bootstrap commit", subject)
	}
	count, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "rev-list", "--count", "HEAD")
	if err != nil || count != "1" {
		t.Fatalf("commit count = %q, %v; want exactly the empty commit", count, err)
	}
	owner, err := execAsGem(t, created.ID, "stat", "-c", "%U", "/workspace/repo/.git")
	if err != nil {
		t.Fatalf("stat .git: %v: %s", err, owner)
	}
	if owner != "gem" {
		t.Fatalf(".git is owned by %q, want gem: the sandbox user could not stage in it", owner)
	}

	// The failure that was reported was stage/commit. Prove the checkout is
	// writable by gem by doing exactly that, then use the resulting HEAD as the
	// marker an idempotent Ensure must not move.
	head, err := execAsGem(t, created.ID, "sh", "-c",
		"printf 'from the sandbox user\\n' > /workspace/repo/notes.txt && "+
			"git -C /workspace/repo add -A && "+
			"git -C /workspace/repo commit -q -m 'test: a commit from the sandbox user' && "+
			"git -C /workspace/repo rev-parse HEAD")
	if err != nil {
		t.Fatalf("stage and commit as gem: %v: %s", err, head)
	}
	author, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "log", "-1", "--format=%an|%ae")
	if err != nil {
		t.Fatalf("author as gem: %v: %s", err, author)
	}
	if author != sandboxGitName+"|"+sandboxGitEmail {
		t.Fatalf("commit author = %q, want the container identity %q", author, sandboxGitName+"|"+sandboxGitEmail)
	}

	// Ensure is called repeatedly (every session creation may reach it). An
	// existing repository with a branch and commits must be left alone: no re-init,
	// no reset, no second empty commit.
	again, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure twice: %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("second Ensure changed the id: %q vs %q", again.ID, created.ID)
	}
	after, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD after second Ensure: %v: %s", err, after)
	}
	if after != head {
		t.Fatalf("HEAD moved on a repeated Ensure: %s -> %s", head, after)
	}
	afterCount, err := execAsGem(t, created.ID, "git", "-C", "/workspace/repo", "rev-list", "--count", "HEAD")
	if err != nil || afterCount != "2" {
		t.Fatalf("commit count after second Ensure = %q, %v; want 2 (nothing added)", afterCount, err)
	}
}

// When the project has a repo_url, Ensure has to clone it and check out the
// requested branch — the other half of what Local does. The seed is a local
// repository created inside the same container, so the test needs no network and
// still runs the exact `git clone` argv the provider uses.
func TestDockerProviderClonesTheRepository(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_clone", "prj_clone"

	// The container (and its mount) has to exist before a seed can be written into
	// it; Ensure would create the checkout we are about to replace, which is fine.
	created, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })

	seed, err := execAsGem(t, created.ID, "sh", "-c",
		"rm -rf /workspace/repo /workspace/seed && "+
			"git init -q -b main /workspace/seed && "+
			"git -C /workspace/seed -c user.name=Seed -c user.email=seed@piforge.local commit --allow-empty -q -m 'seed: main' && "+
			"git -C /workspace/seed checkout -q -b develop && "+
			"printf 'from the seed\\n' > /workspace/seed/seed.txt && "+
			"git -C /workspace/seed add -A && "+
			"git -C /workspace/seed -c user.name=Seed -c user.email=seed@piforge.local commit -q -m 'seed: develop' && "+
			"git -C /workspace/seed rev-parse HEAD")
	if err != nil {
		t.Fatalf("seed repository: %v: %s", err, seed)
	}

	// A fresh Ensure of the same sandbox with a repo_url now has no checkout to
	// re-adopt, so it must clone. The branch requested is not the clone's default,
	// which is the case Local's checkoutBranch handles.
	cloned, err := provider.Ensure(ctx, userID, projectID, "/workspace/seed", "develop")
	if err != nil {
		t.Fatalf("Ensure with repo_url: %v", err)
	}
	if cloned.ID != created.ID {
		t.Fatalf("clone used another container: %q vs %q", cloned.ID, created.ID)
	}

	branch, err := execAsGem(t, cloned.ID, "git", "-C", "/workspace/repo", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("branch after clone: %v: %s", err, branch)
	}
	if branch != "develop" {
		t.Fatalf("branch = %q, want the requested develop", branch)
	}
	head, err := execAsGem(t, cloned.ID, "git", "-C", "/workspace/repo", "rev-parse", "HEAD")
	if err != nil || head != seed {
		t.Fatalf("cloned HEAD = %q, %v; want the seed's develop commit %q", head, err, seed)
	}
	if out, err := execAsGem(t, cloned.ID, "sh", "-c", "test -f /workspace/repo/seed.txt && echo yes"); err != nil || out != "yes" {
		t.Fatalf("cloned worktree is missing the seeded file: out=%q err=%v", out, err)
	}
	if owner, err := execAsGem(t, cloned.ID, "stat", "-c", "%U", "/workspace/repo/.git"); err != nil || owner != "gem" {
		t.Fatalf(".git after clone is owned by %q (%v), want gem", owner, err)
	}
}

// A checkout that cannot be provisioned is a failed Ensure, not a READY sandbox
// with a broken workspace, and the error says which step failed. The branch name
// is one git itself refuses, so the init step is the one that fails.
func TestDockerProviderCheckoutFailureNamesTheStep(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_badinit", "prj_badinit"

	sb, err := provider.Ensure(ctx, userID, projectID, "", "not a branch")
	if err == nil {
		t.Fatalf("Ensure returned %+v for an init that cannot work", sb)
	}
	if !strings.Contains(err.Error(), "init /workspace/repo") {
		t.Fatalf("error = %q, want the failing step named", err)
	}
	t.Logf("Ensure refused with: %v", err)
	// The container was created by this call and cannot serve anything: it must not
	// be left behind for the next Ensure to re-adopt as if it were healthy.
	if _, ierr := provider.inspect(ctx, provider.containerName(userID, projectID)); ierr == nil {
		t.Fatal("a container whose checkout failed was left behind")
	}
}
