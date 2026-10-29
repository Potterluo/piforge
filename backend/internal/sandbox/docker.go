package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// containerWorkspace is where DockerConfig.DataRoot is mounted.
const containerWorkspace = "/workspace"

// Docker is a sandbox provider that runs each (user, project) in its own container.
//
// This exists because the local provider runs the agent, the browser and the terminal on
// the host: isolation is a directory and a convention. A container makes the boundary a
// real one, and an all-in-one image (browser + shell + file service) puts the page the
// human watches and the files the agent edits on the same machine — the property the
// panel currently gets by remembering to keep two sets of paths aligned.
//
// The control plane still never executes untrusted code: it talks to Docker, and the
// container gets a short-lived per-session token rather than a provider key.
//
// Scope of this provider: the lifecycle (create / pause / resume / destroy / exec) and
// the workspace layout. The browser and terminal can attach to the container's services
// (see internal/browser and internal/term); the control plane's workspace IO reads the
// bind-mounted host directory unless -sandbox-files remote selects the container's file
// API (see internal/workspaceremote).
type Docker struct {
	cfg DockerConfig
}

// DockerConfig configures the container provider.
type DockerConfig struct {
	// Image is the sandbox image to run.
	Image string
	// Network is the Docker network name; empty uses the default bridge.
	Network string
	// CPUs and Memory cap the container. Empty means "no limit", which is the honest
	// default for a self-hosted single-user box and a bad one for a shared deployment.
	CPUs   string
	Memory string
	// DataRoot is the host directory bind-mounted at /workspace, so a page refresh or a
	// pause does not lose the checkout.
	DataRoot string
	// APIPort is the container port that serves the sandbox HTTP API (8080 for AIO
	// Sandbox). Zero means "do not publish anything", which is what the lifecycle-only
	// mode wants.
	APIPort int
	// APIKey is passed to the container as SANDBOX_API_KEY when non-empty, so every
	// service in it requires the header.
	APIKey string
}

// NewDocker builds a container provider.
func NewDocker(cfg DockerConfig) (*Docker, error) {
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("sandbox: docker: image is required")
	}
	if strings.TrimSpace(cfg.DataRoot) == "" {
		return nil, fmt.Errorf("sandbox: docker: data root is required")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("sandbox: docker: the docker CLI is not on PATH: %w", err)
	}
	return &Docker{cfg: cfg}, nil
}

// Name identifies the provider in the sandbox row.
func (d *Docker) Name() string { return "docker" }

// containerName is derived from (user, project), for the same reason the local provider
// does it: two sessions in one project must share one sandbox, and the name has to be
// recoverable after a restart of the control plane.
func (d *Docker) containerName(userID, projectID string) string {
	sum := sha256.Sum256([]byte(userID + "/" + projectID))
	return "piforge-" + hex.EncodeToString(sum[:8])
}

// Ensure creates the container if it is missing and returns its state.
func (d *Docker) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	name := d.containerName(userID, projectID)
	if state, err := d.inspect(ctx, name); err == nil {
		if state == "running" {
			return d.describe(ctx, userID, projectID, name)
		}
		if _, err := d.docker(ctx, "start", name); err != nil {
			return nil, err
		}
		return d.describe(ctx, userID, projectID, name)
	}

	args := []string{
		"run", "-d",
		"--name", name,
		"--label", "piforge.user=" + userID,
		"--label", "piforge.project=" + projectID,
		"--workdir", containerWorkspace,
		"-v", d.cfg.DataRoot + ":" + containerWorkspace,
		"--restart", "unless-stopped",
	}
	if d.cfg.Network != "" {
		args = append(args, "--network", d.cfg.Network)
	}
	if d.cfg.CPUs != "" {
		args = append(args, "--cpus", d.cfg.CPUs)
	}
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.APIPort > 0 {
		// Publish on a free host port: two sandboxes on one machine must not fight over
		// 8080, and the control plane reads the assigned port back from `docker port`.
		args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", d.cfg.APIPort))
		// The image starts its Chromium under Docker's default seccomp profile, and the
		// browser dies before CDP opens: "Failed to move to new namespace ... Operation
		// not permitted" (zygote_host_impl_linux.cc:207). BROWSER_NO_SANDBOX is the
		// image's own switch (gem.sh folds it into BROWSER_EXTRA_ARGS), and turning
		// Chrome's sandbox off is the smaller change than --security-opt
		// seccomp=unconfined: the container keeps its syscall filter, and the renderer
		// stays behind the container boundary, which is the boundary this provider
		// promises.
		args = append(args, "-e", "BROWSER_NO_SANDBOX=--no-sandbox")
	}
	if d.cfg.APIKey != "" {
		args = append(args, "-e", "SANDBOX_API_KEY="+d.cfg.APIKey)
	}
	args = append(args, d.cfg.Image)
	// The image may or may not have an entrypoint that idles; a sandbox has to stay up
	// between prompts, so keep it alive explicitly.
	args = append(args, "sleep", "infinity")

	if _, err := d.docker(ctx, args...); err != nil {
		return nil, err
	}
	return d.describe(ctx, userID, projectID, name)
}

// Get reports the current state of the container.
func (d *Docker) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	name := sandboxID
	if !strings.HasPrefix(name, "piforge-") {
		name = "piforge-" + sandboxID
	}
	state, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	out := &contracts.Sandbox{
		ID:       name,
		Provider: d.Name(),
		Image:    d.cfg.Image,
		RootDir:  d.cfg.DataRoot,
	}
	out.State = dockerState(state)
	return out, nil
}

// Pause stops the container. A stopped container keeps its filesystem and its volumes,
// which is what "paused, not deleted" has to mean.
func (d *Docker) Pause(ctx context.Context, sandboxID string) error {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "stop", "--time", "10", name); err != nil {
		return err
	}
	return nil
}

// Resume starts a stopped container.
func (d *Docker) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "start", name); err != nil {
		return nil, err
	}
	return d.Get(ctx, sandboxID)
}

// Destroy removes the container. The workspace volume is left alone on purpose: deleting
// somebody's uncommitted work is not something a sandbox teardown should decide.
func (d *Docker) Destroy(ctx context.Context, sandboxID string) error {
	name := d.resolve(sandboxID)
	if _, err := d.docker(ctx, "rm", "-f", name); err != nil {
		return err
	}
	return nil
}

// Exec runs a command and returns its combined output.
func (d *Docker) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: docker: exec needs a command")
	}
	name := d.resolve(sandboxID)
	args := []string{"exec"}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	args = append(args, name)
	args = append(args, argv...)
	return d.docker(ctx, args...)
}

// describe builds the sandbox record the store keeps.
func (d *Docker) describe(ctx context.Context, userID, projectID, name string) (*contracts.Sandbox, error) {
	state, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	return &contracts.Sandbox{
		ID:        name,
		UserID:    userID,
		ProjectID: projectID,
		State:     dockerState(state),
		Provider:  d.Name(),
		Image:     d.cfg.Image,
		// RootDir is the host side of the bind mount, because the control plane's own
		// git and file IO runs here and has to write where the container looks.
		// ContainerPath translates it to /workspace for anything that speaks to the
		// container. Reporting the container path made provisioning create the repo
		// on the host at "/workspace", i.e. outside the mount: the container then saw
		// an empty workspace.
		RootDir:      d.cfg.DataRoot,
		CreatedAt:    time.Now().UTC(),
		LastActiveAt: time.Now().UTC(),
	}, nil
}

func (d *Docker) resolve(sandboxID string) string {
	if strings.HasPrefix(sandboxID, "piforge-") {
		return sandboxID
	}
	return "piforge-" + sandboxID
}

// inspect returns the container's status string ("running", "exited", ...).
func (d *Docker) inspect(ctx context.Context, name string) (string, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		return "", err
	}
	var state struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
		return "", fmt.Errorf("sandbox: docker: inspect %s: %w", name, err)
	}
	if state.Running {
		return "running", nil
	}
	return state.Status, nil
}

// docker runs the CLI and turns a non-zero exit into an error carrying its output.
func (d *Docker) docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(text, "No such object") || strings.Contains(text, "No such container") {
			return "", fmt.Errorf("%w: %s", ErrNotFound, d.lastArg(args))
		}
		return "", fmt.Errorf("sandbox: docker %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (d *Docker) lastArg(args []string) string {
	for index := len(args) - 1; index >= 0; index-- {
		if !strings.HasPrefix(args[index], "-") && args[index] != "inspect" && args[index] != "start" {
			return args[index]
		}
	}
	return ""
}

// dockerState maps Docker's status to our state machine.
func dockerState(status string) contracts.SandboxState {
	switch strings.TrimSpace(status) {
	case "running":
		return contracts.SandboxReady
	case "created", "restarting":
		return contracts.SandboxStarting
	case "paused":
		return contracts.SandboxPaused
	case "exited", "dead":
		return contracts.SandboxPaused
	case "":
		return contracts.SandboxMissing
	default:
		return contracts.SandboxReady
	}
}

// Endpoint reports the host address that serves the sandbox API for one sandbox.
//
// It asks Docker rather than remembering: the port is assigned at creation and survives
// a control-plane restart, and a number kept in memory would be wrong after either.
func (d *Docker) Endpoint(ctx context.Context, sandboxID string) (string, error) {
	if d.cfg.APIPort <= 0 {
		return "", fmt.Errorf("sandbox: docker: no api port is published for this provider")
	}
	name := d.resolve(sandboxID)
	out, err := d.docker(ctx, "port", name, fmt.Sprintf("%d/tcp", d.cfg.APIPort))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.Split(out, "\n")[0])
	address := strings.TrimPrefix(line, "0.0.0.0:")
	address = strings.TrimPrefix(address, "127.0.0.1:")
	address = strings.TrimPrefix(address, ":::")
	if address == "" {
		return "", fmt.Errorf("sandbox: docker: %s publishes nothing on %d", name, d.cfg.APIPort)
	}
	return "http://127.0.0.1:" + address, nil
}

// ContainerPath maps a host path under DataRoot to the path the same file has
// inside the container.
//
// The terminal needs it: the store knows a worktree's host path, while the
// container shell must start in the mounted path. Anything outside the mount is
// an error rather than a best guess.
func (d *Docker) ContainerPath(hostPath string) (string, error) {
	// A sandbox record may already carry a container path: provision lays out
	// repositories from RootDir, which is containerWorkspace.
	if clean := path.Clean(filepath.ToSlash(strings.TrimSpace(hostPath))); clean == containerWorkspace ||
		strings.HasPrefix(clean, containerWorkspace+"/") {
		return clean, nil
	}

	root, err := filepath.Abs(d.cfg.DataRoot)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: data root %q: %w", d.cfg.DataRoot, err)
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: path %q: %w", hostPath, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("sandbox: docker: %q relative to %q: %w", abs, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: docker: %q is outside the workspace %q", abs, root)
	}
	if rel == "." {
		return containerWorkspace, nil
	}
	return path.Join(containerWorkspace, filepath.ToSlash(rel)), nil
}
