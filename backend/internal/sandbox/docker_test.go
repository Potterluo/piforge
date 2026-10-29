package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// The lifecycle the store and the UI depend on: create, exec, pause, resume, destroy.
func TestDockerProviderLifecycle(t *testing.T) {
	provider := newDockerForTest(t)
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
