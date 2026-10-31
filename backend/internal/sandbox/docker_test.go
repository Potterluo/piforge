package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
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
		// Ensure provisions the checkout through the container's HTTP API, so the
		// port has to be published even though these tests never use the browser.
		APIPort: 8080,
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
// Each container mounts only its own project's directory, so the pair of leading
// segments under DataRoot is what maps to /workspace.
func TestContainerPath(t *testing.T) {
	root := t.TempDir()
	provider := &Docker{cfg: DockerConfig{DataRoot: filepath.Join(root, "sandboxes")}}
	workspace := filepath.Join(provider.cfg.DataRoot, "usr_1", "prj_1")

	got, err := provider.ContainerPath(filepath.Join(workspace, "repo", "worktrees", "feat"))
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if got != "/workspace/repo/worktrees/feat" {
		t.Fatalf("mapped = %q", got)
	}

	// The mounted directory itself is /workspace, not /workspace/<user>/<project>.
	got, err = provider.ContainerPath(workspace)
	if err != nil || got != "/workspace" {
		t.Fatalf("workspace root = %q, %v", got, err)
	}

	// A sandbox record may already carry the container path.
	got, err = provider.ContainerPath("/workspace/repo")
	if err != nil || got != "/workspace/repo" {
		t.Fatalf("already-mapped = %q, %v", got, err)
	}

	if _, err := provider.ContainerPath(t.TempDir()); err == nil {
		t.Fatal("a path outside the data root was accepted")
	}
	// These are no longer mounted anywhere: mapping them to /workspace would
	// silently name the wrong project's tree.
	for _, shared := range []string{
		provider.cfg.DataRoot,
		filepath.Join(provider.cfg.DataRoot, "usr_1"),
	} {
		if got, err := provider.ContainerPath(shared); err == nil {
			t.Fatalf("shared path %q mapped to %q; only a project workspace is mounted", shared, got)
		}
	}
}

// The path derivation is the tenancy boundary, so it is pinned without Docker:
// two pairs must never produce one directory, the same pair must be stable across
// calls (and therefore restarts), and identifiers must not be able to escape the
// data root.
func TestDockerWorkspacePathIsPerPair(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandboxes")
	provider := &Docker{cfg: DockerConfig{DataRoot: root}}

	pairs := []struct{ user, project string }{
		{"usr_a", "prj_one"},
		{"usr_a", "prj_two"},
		{"usr_b", "prj_one"},
	}
	seen := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		dir, err := provider.workspacePath(pair.user, pair.project)
		if err != nil {
			t.Fatalf("workspacePath(%q, %q): %v", pair.user, pair.project, err)
		}
		if want := filepath.Join(root, pair.user, pair.project); dir != want {
			t.Fatalf("workspacePath(%q, %q) = %q, want %q", pair.user, pair.project, dir, want)
		}
		if !isInside(root, dir) {
			t.Fatalf("%q is not inside the data root %q", dir, root)
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("%q escapes the data root as %q", dir, rel)
		}
		for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
			if segment == ".." {
				t.Fatalf("%q escapes the data root as %q", dir, rel)
			}
		}
		if other, ok := seen[dir]; ok {
			t.Fatalf("%s and %s resolve to the same directory %q", other, pair.user+"/"+pair.project, dir)
		}
		seen[dir] = pair.user + "/" + pair.project

		// Stability: the same pair always resolves to the same directory.
		again, err := provider.workspacePath(pair.user, pair.project)
		if err != nil || again != dir {
			t.Fatalf("workspacePath is not stable: %q then %q (%v)", dir, again, err)
		}
	}

	// The container name is derived from the same pair and follows the same rules.
	name := provider.containerName("usr_a", "prj_one")
	if name != provider.containerName("usr_a", "prj_one") {
		t.Fatal("containerName is not stable")
	}
	for _, other := range []string{
		provider.containerName("usr_a", "prj_two"),
		provider.containerName("usr_b", "prj_one"),
	} {
		if name == other {
			t.Fatalf("different pairs share the container name %q", name)
		}
	}

	// No identifier may become a path segment traversal or an absolute/drive path.
	for _, id := range []string{
		"", ".", "..", "../etc", `..\etc`, "a/b", `a\b`,
		"C:", "c:", "a:b", "C:\\Windows", "a b", "a\x00b",
	} {
		if _, err := provider.workspacePath(id, "prj_one"); err == nil {
			t.Fatalf("user id %q was accepted", id)
		}
		if _, err := provider.workspacePath("usr_a", id); err == nil {
			t.Fatalf("project id %q was accepted", id)
		}
	}
}

func containerID(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.Id}}", name).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

// Two different projects must get different host directories and different
// containers, and neither container may see a file written into the other's
// workspace. This is the tenancy boundary: one project's uncommitted work, its
// .env or its commit history must not be visible from another project's sandbox.
func TestDockerProviderSeparatesProjectWorkspaces(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID = "usr_tenancy"

	a, err := provider.Ensure(ctx, userID, "prj_tenancy_a", "", "main")
	if err != nil {
		t.Fatalf("Ensure A: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), a.ID) })
	b, err := provider.Ensure(ctx, userID, "prj_tenancy_b", "", "main")
	if err != nil {
		t.Fatalf("Ensure B: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), b.ID) })

	if a.ID == b.ID {
		t.Fatalf("two projects share the container %q", a.ID)
	}
	wantA := filepath.Join(provider.cfg.DataRoot, userID, "prj_tenancy_a")
	wantB := filepath.Join(provider.cfg.DataRoot, userID, "prj_tenancy_b")
	if !sameHostDir(a.RootDir, wantA) {
		t.Fatalf("A RootDir = %q, want %q", a.RootDir, wantA)
	}
	if !sameHostDir(b.RootDir, wantB) {
		t.Fatalf("B RootDir = %q, want %q", b.RootDir, wantB)
	}
	if sameHostDir(a.RootDir, b.RootDir) {
		t.Fatalf("two projects share the host directory %q", a.RootDir)
	}

	mountA, err := provider.workspaceMount(ctx, a.ID)
	if err != nil {
		t.Fatalf("A mount: %v", err)
	}
	mountB, err := provider.workspaceMount(ctx, b.ID)
	if err != nil {
		t.Fatalf("B mount: %v", err)
	}
	if !sameHostDir(mountA, wantA) || !sameHostDir(mountB, wantB) {
		t.Fatalf("mounts = %q and %q, want %q and %q", mountA, mountB, wantA, wantB)
	}
	if sameHostDir(mountA, mountB) {
		t.Fatalf("both containers mount %q", mountA)
	}

	// Write through the host side (the path RootDir promises the control plane can
	// write into) and read through each container's /workspace.
	if err := os.WriteFile(filepath.Join(a.RootDir, "only-a.txt"), []byte("from project A\n"), 0o644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(b.RootDir, "only-b.txt"), []byte("from project B\n"), 0o644); err != nil {
		t.Fatalf("write B: %v", err)
	}
	checks := []struct {
		container string
		own       string
		foreign   string
	}{
		{a.ID, "/workspace/only-a.txt", "/workspace/only-b.txt"},
		{b.ID, "/workspace/only-b.txt", "/workspace/only-a.txt"},
	}
	for _, check := range checks {
		out, err := execAsGem(t, check.container,
			"sh", "-c", "test -f "+check.own+" && test ! -e "+check.foreign+" && echo isolated")
		if err != nil || out != "isolated" {
			t.Fatalf("%s: own=%s foreign=%s out=%q err=%v",
				check.container, check.own, check.foreign, out, err)
		}
	}
}

// The same (user, project) twice must resolve to the same container and the same
// host directory: Ensure is called for every session, so a second call may not
// create a second checkout (or adopt a different one).
func TestDockerProviderReusesTheProjectWorkspace(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_stable", "prj_stable"

	first, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), first.ID) })
	want := filepath.Join(provider.cfg.DataRoot, userID, projectID)
	if !sameHostDir(first.RootDir, want) {
		t.Fatalf("RootDir = %q, want %q", first.RootDir, want)
	}

	mountBefore, err := provider.workspaceMount(ctx, first.ID)
	if err != nil {
		t.Fatalf("mount before: %v", err)
	}

	again, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure twice: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("a second Ensure made a second container: %q vs %q", again.ID, first.ID)
	}
	if !sameHostDir(again.RootDir, first.RootDir) {
		t.Fatalf("RootDir moved across Ensure: %q -> %q", first.RootDir, again.RootDir)
	}
	if again.State != contracts.SandboxReady {
		t.Fatalf("state after second Ensure = %q", again.State)
	}

	mountAfter, err := provider.workspaceMount(ctx, again.ID)
	if err != nil {
		t.Fatalf("mount after: %v", err)
	}
	if !sameHostDir(mountBefore, mountAfter) {
		t.Fatalf("mount moved across Ensure: %q -> %q", mountBefore, mountAfter)
	}

	// Get reads the mount back, so a restart re-adopts the same directory.
	live, err := provider.Get(ctx, again.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !sameHostDir(live.RootDir, first.RootDir) {
		t.Fatalf("Get RootDir = %q, want %q", live.RootDir, first.RootDir)
	}

	names, err := exec.Command("docker", "ps", "-a", "--filter",
		"label=piforge.project="+projectID, "--format", "{{.Names}}").CombinedOutput()
	if err != nil {
		t.Fatalf("docker ps: %v: %s", err, strings.TrimSpace(string(names)))
	}
	if got := strings.Fields(string(names)); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("containers for the project = %q, want exactly %q", got, first.ID)
	}
}

// A container from before the per-project layout mounts the shared data root. On
// the next Ensure it must be replaced, not reused: reusing it would hand this
// project another project's tree. The old tree stays on disk — recreation is a
// container decision, never a reason to delete somebody's work.
func TestDockerProviderRecreatesAContainerWithTheWrongMount(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_legacy", "prj_legacy"
	name := provider.containerName(userID, projectID)
	want := filepath.Join(provider.cfg.DataRoot, userID, projectID)

	// The pre-fix deployment: this project's container mounted the shared root,
	// and the shared root already contains a checkout with uncommitted work.
	legacyRepo := filepath.Join(provider.cfg.DataRoot, "repo")
	if err := os.MkdirAll(legacyRepo, 0o755); err != nil {
		t.Fatalf("create legacy repo: %v", err)
	}
	precious := filepath.Join(legacyRepo, "precious.txt")
	if err := os.WriteFile(precious, []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	out, err := exec.Command("docker", "run", "-d",
		"--name", name,
		"--workdir", containerWorkspace,
		"-v", provider.cfg.DataRoot+":"+containerWorkspace,
		provider.cfg.Image, "sleep", "infinity").CombinedOutput()
	if err != nil {
		t.Fatalf("run legacy container: %v: %s", err, strings.TrimSpace(string(out)))
	}
	oldID := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", oldID).Run() })

	oldMount, err := provider.workspaceMount(ctx, name)
	if err != nil {
		t.Fatalf("legacy mount: %v", err)
	}
	if !sameHostDir(oldMount, provider.cfg.DataRoot) {
		t.Fatalf("legacy container mounts %q, want the shared root %q", oldMount, provider.cfg.DataRoot)
	}
	// Root, not gem: this container was just started, so the image's entrypoint may
	// not have created the sandbox user yet. The assertion is about the mount, and
	// Ensure's checkout path already waits for gem on the replacement.
	if out, err := exec.Command("docker", "exec", name, "cat", "/workspace/repo/precious.txt").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "uncommitted work" {
		t.Fatalf("legacy container cannot see the old tree: %q %v", strings.TrimSpace(string(out)), err)
	}

	sb, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure over a stale container: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), sb.ID) })
	if !sameHostDir(sb.RootDir, want) {
		t.Fatalf("RootDir = %q, want %q", sb.RootDir, want)
	}

	newID := containerID(t, name)
	if newID == oldID {
		t.Fatal("the container with the wrong mount was reused")
	}
	if _, err := exec.Command("docker", "inspect", oldID).CombinedOutput(); err == nil {
		t.Fatal("the old container still exists after Ensure")
	}
	mount, err := provider.workspaceMount(ctx, name)
	if err != nil {
		t.Fatalf("new mount: %v", err)
	}
	if !sameHostDir(mount, want) {
		t.Fatalf("new container mounts %q, want %q", mount, want)
	}

	// The old directory and its file are still there: no user data was deleted.
	if info, err := os.Stat(legacyRepo); err != nil || !info.IsDir() {
		t.Fatalf("old directory is gone: %v", err)
	}
	if body, err := os.ReadFile(precious); err != nil || string(body) != "uncommitted work\n" {
		t.Fatalf("old file changed: %q %v", body, err)
	}

	// And the new container does not adopt the old tree: its /workspace is the
	// pair's own directory, not the shared root that held precious.txt.
	if seen, err := execAsGem(t, name, "sh", "-c",
		"test ! -e /workspace/precious.txt && test ! -e /workspace/repo/precious.txt && echo clean"); err != nil || seen != "clean" {
		t.Fatalf("new container still sees the old tree: %q %v", seen, err)
	}
}

// recordedShellCall is one command the checkout asked the sandbox to run.
type recordedShellCall struct {
	command string
	cwd     string
}

// fakeCheckoutShell records commands and answers the readiness probes. It has no
// Docker connection at all, which is how the test below proves the checkout path
// needs neither the CLI nor a local daemon.
type fakeCheckoutShell struct {
	mu    sync.Mutex
	calls []recordedShellCall
	ready bool
	// worktreeReady answers probeWorktreeReady. It is separate from ready because
	// the checkout and a branch worktree are two different paths, and a test must
	// be able to say "the checkout is ready but the worktree is not" — the shape a
	// fresh branch session has.
	worktreeReady bool
}

func (f *fakeCheckoutShell) Exec(ctx context.Context, command, cwd string) (aio.ShellResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, recordedShellCall{command: command, cwd: cwd})
	f.mu.Unlock()
	if strings.Contains(command, "piforge-api-ready") {
		return aio.ShellResult{Output: "piforge-api-ready"}, nil
	}
	// The worktree probe also ends in `echo ready`, so it is matched first.
	if strings.Contains(command, "/worktrees/") && strings.Contains(command, "echo ready") {
		if f.worktreeReady {
			return aio.ShellResult{Output: "ready"}, nil
		}
		return aio.ShellResult{Output: "absent"}, nil
	}
	if strings.Contains(command, "echo ready") {
		if f.ready {
			return aio.ShellResult{Output: "ready"}, nil
		}
		return aio.ShellResult{Output: "absent"}, nil
	}
	return aio.ShellResult{ExitCode: 0}, nil
}

// recordingCheckoutShell wraps the real AIO-backed shell so a Docker test can
// inspect the commands that actually crossed /v1/shell/exec.
type recordingCheckoutShell struct {
	inner checkoutShell
	mu    *sync.Mutex
	calls *[]recordedShellCall
}

func (r recordingCheckoutShell) Exec(ctx context.Context, command, cwd string) (aio.ShellResult, error) {
	r.mu.Lock()
	*r.calls = append(*r.calls, recordedShellCall{command: command, cwd: cwd})
	r.mu.Unlock()
	return r.inner.Exec(ctx, command, cwd)
}

// The checkout is provisioned by commands sent to the sandbox's HTTP API, as the
// image's own shell user. This test has no container and no Docker daemon: the
// only thing the provider can reach is the injected shell, so a `docker exec`
// anywhere on the path would fail here.
func TestDockerProviderCheckoutUsesTheSandboxAPI(t *testing.T) {
	const name = "piforge-apishell"

	newProvider := func(t *testing.T, shell *fakeCheckoutShell) *Docker {
		t.Helper()
		provider := &Docker{cfg: DockerConfig{DataRoot: filepath.Join(t.TempDir(), "sandboxes")}}
		provider.openCheckout = func(ctx context.Context, sandboxID string) (checkoutShell, error) {
			if sandboxID != name {
				t.Fatalf("checkout asked for sandbox %q, want %q", sandboxID, name)
			}
			return shell, nil
		}
		return provider
	}

	t.Run("a fresh checkout", func(t *testing.T) {
		shell := &fakeCheckoutShell{}
		provider := newProvider(t, shell)
		if err := provider.ensureCheckout(context.Background(), name, "sandbox-key", "", "main"); err != nil {
			t.Fatalf("ensureCheckout: %v", err)
		}
		calls := shell.calls
		if len(calls) != 7 {
			t.Fatalf("ran %d commands, want the probe, mkdir, four checkout steps and the permissions step: %+v", len(calls), calls)
		}
		if !strings.Contains(calls[0].command, "rev-parse --verify --quiet HEAD") || calls[0].cwd != containerWorkspace {
			t.Fatalf("first command = %+v, want the readiness probe in /workspace", calls[0])
		}
		if calls[1].command != "mkdir -p -- '/workspace/repo'" || calls[1].cwd != containerWorkspace {
			t.Fatalf("prepare command = %+v, want mkdir of the checkout", calls[1])
		}
		want := []string{
			`'git' '-c' 'safe.directory=/workspace/repo' 'init' '-q' '-b' 'main'`,
			`'git' '-c' 'safe.directory=/workspace/repo' 'config' 'user.name' 'PiForge Sandbox'`,
			`'git' '-c' 'safe.directory=/workspace/repo' 'config' 'user.email' 'sandbox@piforge.local'`,
			`'git' '-c' 'safe.directory=/workspace/repo' 'commit' '--allow-empty' '--no-gpg-sign' '-q' '-m' 'piforge: initial commit'`,
			// chmod is not git: the -c safe.directory prefix would be parsed as its
			// mode, which the image's chmod rejects with "invalid mode".
			`'chmod' '-R' 'a+rwX' '/workspace'`,
		}
		for i, command := range want {
			call := calls[i+2]
			if call.command != command {
				t.Fatalf("command %d = %q\nwant %q", i+2, call.command, command)
			}
			// The image ignores cwd, so the directory has to be explicit on every
			// command that touches the checkout.
			if call.cwd != containerRepoDir {
				t.Fatalf("command %d runs in %q, want %q", i+2, call.cwd, containerRepoDir)
			}
		}
		for _, call := range calls {
			lower := strings.ToLower(call.command)
			if strings.Contains(lower, "exit") {
				t.Fatalf("checkout sent an explicit exit, which wedges the image's shell: %q", call.command)
			}
			if strings.Contains(lower, "docker") {
				t.Fatalf("checkout command mentions docker: %q", call.command)
			}
		}
	})

	t.Run("a repository with a commit is left alone", func(t *testing.T) {
		shell := &fakeCheckoutShell{ready: true}
		provider := newProvider(t, shell)
		if err := provider.ensureCheckout(context.Background(), name, "sandbox-key", "", "main"); err != nil {
			t.Fatalf("ensureCheckout: %v", err)
		}
		if len(shell.calls) != 1 || !strings.Contains(shell.calls[0].command, "echo ready") {
			t.Fatalf("a ready checkout ran %+v, want only the readiness probe", shell.calls)
		}
	})

	t.Run("no published API port fails instead of shelling out", func(t *testing.T) {
		provider := &Docker{cfg: DockerConfig{DataRoot: filepath.Join(t.TempDir(), "sandboxes")}}
		err := provider.ensureCheckout(context.Background(), name, "sandbox-key", "", "main")
		if err == nil || !strings.Contains(err.Error(), "no api port") {
			t.Fatalf("ensureCheckout = %v, want the missing API port named", err)
		}
		if !strings.Contains(err.Error(), "not a fallback") {
			t.Fatalf("error = %v, want it to say the docker CLI is not a fallback", err)
		}
	})
}

// The real image proves the same path end to end: the commands go through
// /v1/shell/exec (the recorded wrapper is the actual AIO client), and the
// checkout they leave behind is owned by the account that shell runs as.
func TestDockerProviderCheckoutCrossesTheSandboxAPI(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	ctx := context.Background()
	const userID, projectID = "usr_api", "prj_api"

	var mu sync.Mutex
	var calls []recordedShellCall
	provider.openCheckout = func(ctx context.Context, sandboxID string) (checkoutShell, error) {
		inner, err := provider.sandboxShell(ctx, sandboxID, "")
		if err != nil {
			return nil, err
		}
		return recordingCheckoutShell{inner: inner, mu: &mu, calls: &calls}, nil
	}

	created, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), created.ID) })

	mu.Lock()
	recorded := append([]recordedShellCall(nil), calls...)
	mu.Unlock()
	if len(recorded) < 6 {
		t.Fatalf("checkout crossed the API %d times, want the probe, mkdir and steps: %+v", len(recorded), recorded)
	}
	if !strings.Contains(recorded[0].command, "echo ready") {
		t.Fatalf("first API call = %+v, want the readiness probe", recorded[0])
	}
	gitCalls := 0
	for _, call := range recorded {
		if strings.Contains(call.command, "safe.directory="+containerRepoDir) {
			gitCalls++
		}
	}
	if gitCalls < 4 {
		t.Fatalf("only %d of %d API commands carried safe.directory: %+v", gitCalls, len(recorded), recorded)
	}
	t.Logf("checkout ran %d commands through /v1/shell/exec", len(recorded))

	if owner, err := execAsGem(t, created.ID, "stat", "-c", "%U", "/workspace/repo/.git"); err != nil || owner != "gem" {
		t.Fatalf(".git is owned by %q (%v), want gem", owner, err)
	}
}

// The checkout argv is the contract: each case is pinned exactly, because these

// commands run inside the container as `gem` and a silently different list would
// bring back either a missing repository (no commit to branch worktrees from) or a
// checkout only one of the two writers can write to.
//
// The last step is the second writer. The checkout belongs to gem, but the control
// plane materialises tooling into it as root with CAP_DAC_OVERRIDE dropped (that
// drop is what makes `copy-ro` skills true), so the tree has to carry the write bit
// for both. Without it provisioning fails on the first write:
//
//	materialize: create skills root: mkdir .../repo/.pi: permission denied
//
// It must not appear on the ready path: re-running it would also loosen the
// 0444/0555 copies materialise writes, which are the read-only guarantee itself.
//
// The last case is stage 2's: with a detached workspace there is no second writer
// (the projection is written through the sandbox API as gem), so opening the tree
// to a control plane that no longer writes into it would be loosening it for
// nobody.
func TestPlanCheckout(t *testing.T) {
	open := []string{"chmod", "-R", "a+rwX", "/workspace"}
	tests := []struct {
		name     string
		ready    bool
		repoURL  string
		branch   string
		detached bool
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
				open,
			},
			wantName: []string{"init", "init", "init", "commit", "permissions"},
		},
		{
			name:   "the requested branch is the one initialized",
			branch: " develop ",
			want: [][]string{
				{"git", "init", "-q", "-b", "develop"},
				{"git", "config", "user.name", "PiForge Sandbox"},
				{"git", "config", "user.email", "sandbox@piforge.local"},
				{"git", "commit", "--allow-empty", "--no-gpg-sign", "-q", "-m", "piforge: initial commit"},
				open,
			},
			wantName: []string{"init", "init", "init", "commit", "permissions"},
		},
		{
			name:    "repo with url: clone, then checkout with a create fallback",
			repoURL: " https://example.invalid/acme/widgets.git ",
			branch:  "develop",
			want: [][]string{
				{"git", "clone", "--quiet", "--", "https://example.invalid/acme/widgets.git", "."},
				{"git", "checkout", "-q", "develop"},
				open,
			},
			wantName: []string{"clone", "checkout", "permissions"},
			// Local's checkoutBranch creates a branch that does not exist yet rather
			// than failing; the container does the same on the second attempt.
			fallback: []string{"git", "checkout", "-q", "-b", "develop"},
		},
		{
			name:     "detached workspace: one writer, so the tree is not opened",
			repoURL:  "https://example.invalid/acme/widgets.git",
			branch:   "main",
			detached: true,
			want: [][]string{
				{"git", "clone", "--quiet", "--", "https://example.invalid/acme/widgets.git", "."},
				{"git", "checkout", "-q", "main"},
			},
			wantName: []string{"clone", "checkout"},
			fallback: []string{"git", "checkout", "-q", "-b", "main"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := planCheckout(tt.ready, tt.repoURL, tt.branch, !tt.detached)
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
// Every assertion below runs as `gem` — through `docker exec` for inspection
// only; the checkout itself was provisioned over the sandbox API (see
// TestDockerProviderCheckoutUsesTheSandboxAPI).
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

// A branch session's worktree plan is the contract: check the branch name, then
// add the worktree from the repository, with the exact argv and directory each
// step runs with. It is pure, so this test needs no Docker.
func TestPlanBranchWorktree(t *testing.T) {
	const (
		repoDir     = containerRepoDir
		worktreeDir = "/workspace/worktrees/fix-cache"
	)
	tests := []struct {
		name        string
		ready       bool
		detached    bool
		worktreeDir string
		branch      string
		want        [][]string
		wantName    []string
		wantErr     string
	}{
		{
			name:        "fresh branch: validate the name, then add the worktree",
			worktreeDir: worktreeDir,
			branch:      "piforge/fix-cache",
			want: [][]string{
				{"git", "check-ref-format", "--branch", "piforge/fix-cache"},
				{"git", "worktree", "add", "-b", "piforge/fix-cache", "--", worktreeDir},
				// The worktree has the same two writers as the main checkout, so it
				// gets the same write bit for the control plane (see planCheckout).
				{"chmod", "-R", "a+rwX", worktreeDir},
			},
			wantName: []string{"check-ref-format", "worktree", "permissions"},
		},
		{
			name:        "the branch name is trimmed before it reaches git",
			worktreeDir: worktreeDir,
			branch:      "  piforge/fix-cache  ",
			want: [][]string{
				{"git", "check-ref-format", "--branch", "piforge/fix-cache"},
				{"git", "worktree", "add", "-b", "piforge/fix-cache", "--", worktreeDir},
				{"chmod", "-R", "a+rwX", worktreeDir},
			},
			wantName: []string{"check-ref-format", "worktree", "permissions"},
		},
		{
			name:        "detached workspace: the worktree belongs to gem alone",
			detached:    true,
			worktreeDir: worktreeDir,
			branch:      "piforge/fix-cache",
			want: [][]string{
				{"git", "check-ref-format", "--branch", "piforge/fix-cache"},
				{"git", "worktree", "add", "-b", "piforge/fix-cache", "--", worktreeDir},
			},
			wantName: []string{"check-ref-format", "worktree"},
		},
		{
			name:        "an existing worktree is left exactly as it is",
			ready:       true,
			worktreeDir: worktreeDir,
			branch:      "piforge/fix-cache",
		},
		{
			name:        "an empty branch is refused before git runs",
			worktreeDir: worktreeDir,
			branch:      "   ",
			wantErr:     "branch name is required",
		},
		{
			name:        "a branch that looks like an option is refused",
			worktreeDir: worktreeDir,
			branch:      "-b",
			wantErr:     "starts with a dash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := planBranchWorktree(tt.ready, repoDir, tt.worktreeDir, tt.branch, !tt.detached)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("planBranchWorktree error = %v, want %q", err, tt.wantErr)
				}
				if len(steps) != 0 {
					t.Fatalf("a refused plan returned steps: %+v", steps)
				}
				return
			}
			if err != nil {
				t.Fatalf("planBranchWorktree: %v", err)
			}
			var argv [][]string
			var names []string
			for _, step := range steps {
				argv = append(argv, step.argv)
				names = append(names, step.name)
				if step.dir != repoDir {
					t.Fatalf("step %q runs in %q, want the repository %q", step.name, step.dir, repoDir)
				}
			}
			if !reflect.DeepEqual(argv, tt.want) {
				t.Fatalf("argv = %q\nwant    %q", argv, tt.want)
			}
			if !reflect.DeepEqual(names, tt.wantName) {
				t.Fatalf("step names = %q, want %q", names, tt.wantName)
			}
		})
	}
}

// The branch worktree has to be created by the sandbox itself, as the sandbox
// user, over /v1/shell/exec — the defect was a `git worktree add` on the host
// through the bind mount, which left .git/worktrees/<slug> owned by somebody the
// container's git could not write as. This test has no container: the only thing
// the provider can reach is the injected shell, so any host-side git (or a
// `docker exec`) on the path would fail here.
func TestDockerProviderBranchWorktreeUsesTheSandboxAPI(t *testing.T) {
	const name = "piforge-apishell"
	dataRoot := t.TempDir()
	repoHost := filepath.Join(dataRoot, "usr_api", "prj_api", "repo")
	worktreeHost := filepath.Join(dataRoot, "usr_api", "prj_api", "worktrees", "fix-cache")

	newProvider := func(t *testing.T, shell *fakeCheckoutShell) *Docker {
		t.Helper()
		provider := &Docker{cfg: DockerConfig{DataRoot: dataRoot}}
		provider.openCheckout = func(ctx context.Context, sandboxID string) (checkoutShell, error) {
			if sandboxID != name {
				t.Fatalf("worktree asked for sandbox %q, want %q", sandboxID, name)
			}
			return shell, nil
		}
		return provider
	}

	t.Run("a new worktree crosses the API as the sandbox user", func(t *testing.T) {
		shell := &fakeCheckoutShell{}
		provider := newProvider(t, shell)
		if err := provider.AddWorktree(context.Background(), name, repoHost, worktreeHost, "piforge/fix-cache"); err != nil {
			t.Fatalf("AddWorktree: %v", err)
		}
		calls := shell.calls
		if len(calls) != 5 {
			t.Fatalf("ran %d commands, want the liveness probe, the worktree probe, two worktree steps and the permissions step: %+v", len(calls), calls)
		}
		if calls[0].command != "echo piforge-api-ready" || calls[0].cwd != containerWorkspace {
			t.Fatalf("first command = %+v, want the API liveness probe", calls[0])
		}
		if !strings.Contains(calls[1].command, "rev-parse --verify --quiet HEAD") ||
			!strings.Contains(calls[1].command, "safe.directory=/workspace/worktrees/fix-cache") ||
			calls[1].cwd != containerWorkspace {
			t.Fatalf("second command = %+v, want the worktree readiness probe", calls[1])
		}
		want := []recordedShellCall{
			{
				command: `'git' '-c' 'safe.directory=/workspace/repo' 'check-ref-format' '--branch' 'piforge/fix-cache'`,
				cwd:     containerRepoDir,
			},
			{
				command: `'git' '-c' 'safe.directory=/workspace/repo' 'worktree' 'add' '-b' 'piforge/fix-cache' '--' '/workspace/worktrees/fix-cache'`,
				cwd:     containerRepoDir,
			},
			{
				command: `'chmod' '-R' 'a+rwX' '/workspace/worktrees/fix-cache'`,
				cwd:     containerRepoDir,
			},
		}
		for i, want := range want {
			got := calls[i+2]
			if got.command != want.command {
				t.Fatalf("command %d = %q\nwant %q", i+2, got.command, want.command)
			}
			// The image ignores cwd, so the repository has to be explicit on every
			// command that touches it.
			if got.cwd != want.cwd {
				t.Fatalf("command %d runs in %q, want %q", i+2, got.cwd, want.cwd)
			}
		}
		for _, call := range calls {
			lower := strings.ToLower(call.command)
			if strings.Contains(lower, "exit") {
				t.Fatalf("worktree provisioning sent an explicit exit, which wedges the image's shell: %q", call.command)
			}
			if strings.Contains(lower, "docker") {
				t.Fatalf("worktree command mentions docker: %q", call.command)
			}
		}
	})

	t.Run("an existing worktree is not added twice", func(t *testing.T) {
		shell := &fakeCheckoutShell{worktreeReady: true}
		provider := newProvider(t, shell)
		if err := provider.AddWorktree(context.Background(), name, repoHost, worktreeHost, "piforge/fix-cache"); err != nil {
			t.Fatalf("AddWorktree: %v", err)
		}
		if len(shell.calls) != 2 {
			t.Fatalf("a ready worktree ran %+v, want only the two probes", shell.calls)
		}
		for _, call := range shell.calls[1:] {
			if strings.Contains(call.command, "worktree add") {
				t.Fatalf("an existing worktree was added again: %q", call.command)
			}
		}
	})

	t.Run("no published API port fails instead of shelling out", func(t *testing.T) {
		provider := &Docker{cfg: DockerConfig{DataRoot: dataRoot}}
		err := provider.AddWorktree(context.Background(), name, repoHost, worktreeHost, "piforge/fix-cache")
		if err == nil || !strings.Contains(err.Error(), "no api port") {
			t.Fatalf("AddWorktree = %v, want the missing API port named", err)
		}
	})

	// The container path the terminal and the file API use must keep matching the
	// host worktree, or a branch session would show one directory and commit in
	// another.
	if got, err := (&Docker{cfg: DockerConfig{DataRoot: dataRoot}}).ContainerPath(worktreeHost); err != nil || got != "/workspace/worktrees/fix-cache" {
		t.Fatalf("ContainerPath(%q) = %q, %v; want /workspace/worktrees/fix-cache", worktreeHost, got, err)
	}
}

// The `docker run` argv is where the two deployment shapes differ, and it is the
// one place a test can see it without a daemon: a single-host provider publishes a
// loopback port and reads it back, a multi-service provider publishes nothing and
// addresses the container by name on a shared network.
func TestContainerArgs(t *testing.T) {
	single := &Docker{cfg: DockerConfig{
		Image:    "sandbox:1.11.0",
		DataRoot: "/data/sandboxes",
		APIPort:  8080,
		APIKey:   "sandbox-key",
	}}
	args := single.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/data/sandboxes/usr_1/prj_1", "sandbox-key")
	assertArgsContain(t, args, "-v", "/data/sandboxes/usr_1/prj_1:/workspace")
	assertArgsContain(t, args, "-p", "127.0.0.1::8080")
	assertArgsContain(t, args, "-e", "BROWSER_NO_SANDBOX=--no-sandbox")
	assertArgsContain(t, args, "-e", "SANDBOX_API_KEY=sandbox-key")
	assertArgsContain(t, args, "-e", "WORKSPACE=/workspace")
	assertArgsContain(t, args, "--label", "piforge.user=usr_1")
	assertArgsContain(t, args, "--label", "piforge.project=prj_1")
	assertArgsAbsent(t, args, "--network")
	if got := args[len(args)-1]; got != "infinity" {
		t.Fatalf("the container does not idle: last arg %q", got)
	}

	shared := &Docker{cfg: DockerConfig{
		Image:             "sandbox:1.11.0",
		DataRoot:          "/data/sandboxes",
		Network:           "piforge-sandboxes",
		APIPort:           8080,
		APIKey:            "sandbox-key",
		ContainerEndpoint: true,
	}}
	args = shared.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/data/sandboxes/usr_1/prj_1", "sandbox-key")
	assertArgsAbsent(t, args, "-p")
	assertArgsContain(t, args, "--network", "piforge-sandboxes")
	// The browser switch and the key are about the image, not about how the API is
	// reached, so they must survive the endpoint change.
	assertArgsContain(t, args, "-e", "BROWSER_NO_SANDBOX=--no-sandbox")
	assertArgsContain(t, args, "-e", "SANDBOX_API_KEY=sandbox-key")
}

// A containerised control plane cannot name a host path for a bind mount: the
// daemon resolves it on its own host, where the shared volume is not at DataRoot.
// The volume shape mounts the pair's subdirectory of a volume the daemon resolves
// itself, so the argv must carry a volume mount and no -v at all.
func TestContainerArgsVolumeWorkspace(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{
		Image:               "sandbox:1.11.0",
		DataRoot:            "/data/sandboxes",
		Network:             "piforge-sandboxes",
		APIPort:             8080,
		APIKey:              "sandbox-key",
		ContainerEndpoint:   true,
		WorkspaceVolume:     "piforge-data",
		WorkspaceVolumeRoot: "/data",
	}, volumePrefix: "sandboxes"}
	args := provider.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/data/sandboxes/usr_1/prj_1", "sandbox-key")
	// The subpath is relative to the volume's root, not to the data root: the
	// daemon prepends the volume's mountpoint, and a "usr_1/prj_1" here would
	// point at <volume>/usr_1/prj_1 instead of <volume>/sandboxes/usr_1/prj_1.
	assertArgsContain(t, args, "--mount",
		"type=volume,source=piforge-data,target=/workspace,volume-subpath=sandboxes/usr_1/prj_1")
	assertArgsAbsent(t, args, "-v")
	// The image chowns $WORKSPACE to its sandbox user; without this the mount stays
	// root-owned and the checkout cannot be created as gem.
	assertArgsContain(t, args, "-e", "WORKSPACE=/workspace")
	assertArgsContain(t, args, "--network", "piforge-sandboxes")
}

// The subpath is derived from the volume mountpoint, and a data root outside it
// has no subpath to name: refuse that at construction, not on a create.
func TestNewDockerRefusesADataRootOutsideTheVolume(t *testing.T) {
	_, err := NewDockerAPI(DockerConfig{
		DataRoot:            "/srv/sandboxes",
		APIPort:             8080,
		WorkspaceVolume:     "piforge-data",
		WorkspaceVolumeRoot: "/data",
	})
	if err == nil || !strings.Contains(err.Error(), "not inside the workspace volume") {
		t.Fatalf("NewDockerAPI = %v, want an error naming the data root outside the volume", err)
	}
	_, err = NewDockerAPI(DockerConfig{
		DataRoot:        "/data/sandboxes",
		APIPort:         8080,
		WorkspaceVolume: "piforge-data",
	})
	if err == nil || !strings.Contains(err.Error(), "WorkspaceVolumeRoot") {
		t.Fatalf("NewDockerAPI = %v, want an error naming the missing volume root", err)
	}
}

// The volume a container was created with is read back from its own HostConfig,
// because inspect's runtime mounts do not carry a volume subpath. A container from
// another project, or from the bind-mount shape, has to be recognised — it is the
// difference between reusing a sandbox and showing one project another's tree.
func TestParseVolumeWorkspaceMount(t *testing.T) {
	const volume = "piforge-data"
	cases := []struct {
		name    string
		out     string
		want    string
		wantErr string
	}{
		{
			name: "this project's subpath",
			out:  `[{"Type":"volume","Source":"piforge-data","Target":"/workspace","VolumeOptions":{"Subpath":"sandboxes/usr_1/prj_1"}}]`,
			want: "/data/sandboxes/usr_1/prj_1",
		},
		{
			name: "another project's subpath is reported as itself, so Ensure replaces the container",
			out:  `[{"Type":"volume","Source":"piforge-data","Target":"/workspace","VolumeOptions":{"Subpath":"sandboxes/usr_1/prj_2"}}]`,
			want: "/data/sandboxes/usr_1/prj_2",
		},
		{
			name:    "the volume root is not a workspace",
			out:     `[{"Type":"volume","Source":"piforge-data","Target":"/workspace"}]`,
			wantErr: "without a subpath",
		},
		{
			name:    "a bind mount is not this shape",
			out:     `[{"Type":"bind","Source":"/data/sandboxes/usr_1/prj_1","Target":"/workspace"}]`,
			wantErr: "want volume",
		},
		{
			name:    "a different volume is not this shape",
			out:     `[{"Type":"volume","Source":"other","Target":"/workspace","VolumeOptions":{"Subpath":"sandboxes/usr_1/prj_1"}}]`,
			wantErr: "want volume",
		},
		{
			name:    "no /workspace mount at all",
			out:     `[{"Type":"volume","Source":"piforge-data","Target":"/data","VolumeOptions":{"Subpath":"sandboxes/usr_1/prj_1"}}]`,
			wantErr: "no /workspace mount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVolumeWorkspaceMount(tc.out, volume, "/data")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseVolumeWorkspaceMount = %q, %v; want an error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVolumeWorkspaceMount: %v", err)
			}
			if filepath.ToSlash(got) != tc.want {
				t.Fatalf("parseVolumeWorkspaceMount = %q, want %q", got, tc.want)
			}
		})
	}
}

// A container endpoint without a network would be a hostname nothing can resolve;
// refuse it at construction rather than at the first request.
func TestNewDockerRefusesContainerEndpointWithoutNetwork(t *testing.T) {
	_, err := NewDockerAPI(DockerConfig{DataRoot: t.TempDir(), APIPort: 8080, ContainerEndpoint: true})
	if err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("NewDockerAPI = %v, want an error naming the missing network", err)
	}
}

func assertArgsContain(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for index := 0; index < len(args)-1; index++ {
		if args[index] == flag && args[index+1] == value {
			return
		}
	}
	t.Fatalf("args do not contain %q %q: %v", flag, value, args)
}

func assertArgsAbsent(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			t.Fatalf("args contain %q, which this shape must not use: %v", flag, args)
		}
	}
}

// Stage 2 of docs/EXECUTION-BOUNDARY.md: with -agent-in-sandbox the sandbox's
// workspace is a volume of its own, so the argv must mount that volume at
// /workspace with no host path and no shared subpath. Anything else would put the
// tree back on a mount this process can reach.
func TestContainerArgsDetachedWorkspace(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{
		Image:             "sandbox:1.11.0",
		DataRoot:          "/data/sandboxes",
		Network:           "piforge-sandboxes",
		APIPort:           8080,
		APIKey:            "sandbox-key",
		ContainerEndpoint: true,
		DetachedWorkspace: true,
	}}
	args := provider.containerArgs(context.Background(), "piforge-abc12345", "usr_1", "prj_1", "/data/sandboxes/usr_1/prj_1", "sandbox-key")
	assertArgsContain(t, args, "--mount", "type=volume,source=piforge-ws-abc12345,target=/workspace")
	assertArgsAbsent(t, args, "-v")
	for _, arg := range args {
		if strings.Contains(arg, "volume-subpath") {
			t.Fatalf("a detached workspace named a shared subpath: %q", arg)
		}
		if strings.Contains(arg, "/data/sandboxes") {
			t.Fatalf("a detached workspace leaked the control plane's path: %q", arg)
		}
	}
	// The image still has to chown the mount to its sandbox user; that is what
	// makes the checkout creatable as gem without the control plane opening it.
	assertArgsContain(t, args, "-e", "WORKSPACE=/workspace")
}

// A detached container is recognised from its own mount record, not from a flag,
// so a process that adopted it (a restarted control plane, the stateless runner)
// reaches the same answer.
func TestDetachedMountIsRecognised(t *testing.T) {
	const name = "piforge-abc12345"
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "this sandbox's own volume",
			out:  `[{"Type":"volume","Source":"piforge-ws-abc12345","Target":"/workspace"}]`,
			want: true,
		},
		{
			name: "another sandbox's volume is not ours",
			out:  `[{"Type":"volume","Source":"piforge-ws-ffffffff","Target":"/workspace"}]`,
			want: false,
		},
		{
			name: "the shared volume shape carries a subpath",
			out:  `[{"Type":"volume","Source":"piforge-ws-abc12345","Target":"/workspace","VolumeOptions":{"Subpath":"sandboxes/usr_1/prj_1"}}]`,
			want: false,
		},
		{
			name: "a bind mount is the single-host shape",
			out:  `[{"Type":"bind","Source":"/data/sandboxes/usr_1/prj_1","Target":"/workspace"}]`,
			want: false,
		},
		{
			name: "nothing at /workspace",
			out:  `[{"Type":"volume","Source":"piforge-ws-abc12345","Target":"/cache"}]`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detachedMount(tc.out, name); got != tc.want {
				t.Fatalf("detachedMount = %v, want %v", got, tc.want)
			}
		})
	}
}

// A detached workspace built by the sandbox user is not a foreign-owned tree, and
// `safe.directory` disables an ownership check: injecting it where nothing needs
// it would be loosening the sandbox for no reason.
func TestCheckoutCommandInjectsSafeDirectoryOnlyWhenItIsNeeded(t *testing.T) {
	shared, err := checkoutCommand([]string{"git", "status"}, "/workspace/repo", true)
	if err != nil {
		t.Fatalf("checkoutCommand: %v", err)
	}
	if !strings.Contains(shared, "safe.directory=/workspace/repo") {
		t.Fatalf("a shared checkout did not get safe.directory: %s", shared)
	}
	owned, err := checkoutCommand([]string{"git", "status"}, "/workspace/repo", false)
	if err != nil {
		t.Fatalf("checkoutCommand: %v", err)
	}
	if strings.Contains(owned, "safe.directory") {
		t.Fatalf("a sandbox-owned checkout got safe.directory anyway: %s", owned)
	}
	// The command itself is unchanged; only the trust flag differs.
	if owned != `'git' 'status'` {
		t.Fatalf("command = %s", owned)
	}
}

// The two workspace shapes cannot both be true: one says "this process mounts the
// tree", the other says it must not.
func TestNewDockerRefusesDetachedPlusSharedVolume(t *testing.T) {
	_, err := NewDockerAPI(DockerConfig{
		DataRoot:            "/data/sandboxes",
		APIPort:             8080,
		DetachedWorkspace:   true,
		WorkspaceVolume:     "piforge-data",
		WorkspaceVolumeRoot: "/data",
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("NewDockerAPI = %v, want an error naming the contradiction", err)
	}
}

// The reported mode is what every later write branches on, so it has to follow the
// root: a container path names a tree this process cannot open.
func TestWorkspaceModeFollowsTheReportedRoot(t *testing.T) {
	cases := []struct{ root string; want contracts.WorkspaceMode }{
		{"/workspace", contracts.WorkspaceAPI},
		{"/data/sandboxes/usr_1/prj_1", contracts.WorkspaceHost},
	}
	for _, tc := range cases {
		if got := workspaceModeOf(tc.root); got != tc.want {
			t.Fatalf("workspaceModeOf(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}
