package sandboxrunner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// DockerProvider is the runner's Provider: it performs every operation with
// sandbox.Docker, the same implementation the in-process provider uses.
//
// The adapter adds nothing to the container rules. It only merges the runner's
// deployment defaults (image, network, limits, API port, key) with what one
// request asked for, builds the provider for that request, and reports the record
// the control plane expects. Because sandbox.Docker derives the container name
// and the workspace from (user, project) and holds no other state, building one
// per request is equivalent to holding one — and it keeps the runner stateless
// across restarts, which is the property that makes it safe to deploy at all.
type DockerProvider struct {
	opts DockerOptions
}

// DockerOptions is the runner's static configuration.
type DockerOptions struct {
	// DataRoot is the directory that holds one workspace per (user, project).
	//
	// It has to be the same directory the daemon resolves a bind mount against
	// (the runner shares the daemon's host) *and* the same path the control plane
	// sees. The workspace is a bind mount, so a path that differs between the two
	// services is not an error anywhere: Docker quietly creates an empty directory
	// and both sides keep writing to trees the other never reads.
	DataRoot string
	// Image is the default sandbox image; a request may name another one.
	Image string
	// Network is the default Docker network for sandboxes.
	Network string
	// CPUs and Memory are the default container limits; empty means "no limit".
	CPUs   string
	Memory string
	// APIPort is the default container port that serves the sandbox API (8080).
	APIPort int
	// APIKey is a fixed SANDBOX_API_KEY for every container this runner creates,
	// and the key used to reach a container whose own key cannot be read. Empty is
	// the secure default: sandbox.Docker then generates one key per container and
	// reports it in the create record.
	APIKey string
	// ContainerEndpoint makes the provider report http://<container>:<APIPort> on
	// Network instead of a published loopback port. Set it when the control plane
	// is not a host process (see sandbox.DockerConfig.ContainerEndpoint).
	ContainerEndpoint bool
	// WorkspaceVolumeRoot is where that volume is mounted here, so the subpath a
	// sandbox mounts can be computed from DataRoot (see
	// sandbox.DockerConfig.WorkspaceVolumeRoot).
	WorkspaceVolumeRoot string
	// WorkspaceVolume names the Docker volume whose tree is DataRoot, for a
	// deployment where this service is itself a container: the daemon resolves a
	// bind mount's source on its own host, where DataRoot is not the volume the
	// two services share (see sandbox.DockerConfig.WorkspaceVolume). Empty keeps
	// the bind mount.
	WorkspaceVolume string
}

// NewDockerProvider validates the runner's configuration.
func NewDockerProvider(opts DockerOptions) (*DockerProvider, error) {
	if strings.TrimSpace(opts.DataRoot) == "" {
		return nil, fmt.Errorf("sandboxrunner: a data root is required (SANDBOX_DATA_ROOT)")
	}
	root, err := filepath.Abs(opts.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("sandboxrunner: abs data root %q: %w", opts.DataRoot, err)
	}
	opts.DataRoot = filepath.Clean(root)
	// Creating it here fails at startup rather than on the first request, and it is
	// this process that owns the directory: the control plane only reads it.
	if err := os.MkdirAll(opts.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("sandboxrunner: create data root %q: %w", opts.DataRoot, err)
	}
	if opts.APIPort <= 0 {
		opts.APIPort = 8080
	}
	if opts.ContainerEndpoint && strings.TrimSpace(opts.Network) == "" {
		return nil, fmt.Errorf("sandboxrunner: container endpoints need a network (SANDBOX_NETWORK)")
	}
	return &DockerProvider{opts: opts}, nil
}

// Ensure creates or adopts the pair's container and returns its record.
func (p *DockerProvider) Ensure(ctx context.Context, request sandbox.RunnerRequest) (sandbox.RunnerRecord, error) {
	provider, err := p.creator(request)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	sb, err := provider.Ensure(ctx, request.UserID, request.ProjectID, request.RepoURL, request.Branch)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	record, err := p.record(ctx, provider, sb)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	// Say what this process did, not just that it answered a request: the runner is
	// the only process in the deployment that can create a container, and its log
	// is the only place that names the one it made (the key is not logged — it
	// leaves through the create response, once).
	slog.Info("sandbox container is up",
		"container", record.ID, "user", request.UserID, "project", request.ProjectID,
		"state", record.State, "root_dir", record.RootDir, "endpoint", record.Endpoint)
	return record, nil
}

// Get reports the state of one container.
func (p *DockerProvider) Get(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error) {
	provider, err := p.api("")
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	sb, err := provider.Get(ctx, sandboxID)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	return p.record(ctx, provider, sb)
}

// Pause stops the container and keeps its workspace.
func (p *DockerProvider) Pause(ctx context.Context, sandboxID string) error {
	provider, err := p.api("")
	if err != nil {
		return err
	}
	return provider.Pause(ctx, sandboxID)
}

// Resume starts a stopped container and returns its record.
func (p *DockerProvider) Resume(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error) {
	provider, err := p.api("")
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	sb, err := provider.Resume(ctx, sandboxID)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	return p.record(ctx, provider, sb)
}

// Destroy removes the container and leaves the data directory alone.
func (p *DockerProvider) Destroy(ctx context.Context, sandboxID string) error {
	provider, err := p.api("")
	if err != nil {
		return err
	}
	return provider.Destroy(ctx, sandboxID)
}

// AddWorktree creates a branch session's worktree inside the container, as the
// sandbox user, through the container's own API.
func (p *DockerProvider) AddWorktree(ctx context.Context, sandboxID string, request sandbox.RunnerWorktreeRequest) error {
	provider, err := p.api(request.APIKey)
	if err != nil {
		return err
	}
	return provider.AddWorktree(ctx, sandboxID, request.RepoDir, request.Path, request.Branch)
}

// Attach starts a process inside an existing container and hands back its stdio.
//
// It is the seventh operation and the only streaming one. It does not go through
// `p.api` (which builds the *API* client): attach is a Docker CLI operation on
// the container, exactly what `sandbox.Docker.Attach` implements for the
// in-process provider. The container name IS the sandbox id here — the same rule
// that makes the runner stateless — and `docker exec` on a stopped container
// fails with Docker's own message, which the client maps to its own error.
func (p *DockerProvider) Attach(ctx context.Context, sandboxID string, spec sandbox.AttachSpec) (io.ReadWriteCloser, error) {
	provider, err := p.api("")
	if err != nil {
		return nil, err
	}
	return provider.Attach(ctx, sandboxID, spec)
}

// Reattach connects to a process that is already inside an existing container.
//
// It is the eighth operation, added with stage 3 of docs/EXECUTION-BOUNDARY.md:
// the agent now outlives the control plane, so a restarted control plane has to
// reach the supervisor that kept it alive instead of starting a second agent.
// Like Attach it is a Docker CLI operation on the container, so it delegates to
// the same *sandbox.Docker the in-process provider uses.
func (p *DockerProvider) Reattach(ctx context.Context, sandboxID string, spec sandbox.ReattachSpec) (io.ReadWriteCloser, error) {
	provider, err := p.api("")
	if err != nil {
		return nil, err
	}
	return provider.Reattach(ctx, sandboxID, spec)
}

// creator builds a provider that can create a container: image and CLI required.
func (p *DockerProvider) creator(request sandbox.RunnerRequest) (*sandbox.Docker, error) {
	image := firstNonEmpty(request.Image, p.opts.Image)
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("sandboxrunner: no image: the request carried none and the runner has no default (SANDBOX_IMAGE)")
	}
	return sandbox.NewDocker(sandbox.DockerConfig{
		Image:               image,
		Network:             firstNonEmpty(request.Network, p.opts.Network),
		CPUs:                firstNonEmpty(request.CPUs, p.opts.CPUs),
		Memory:              firstNonEmpty(request.Memory, p.opts.Memory),
		DataRoot:            p.opts.DataRoot,
		APIPort:             p.apiPort(request.APIPort),
		APIKey:              firstNonEmpty(request.APIKey, p.opts.APIKey),
		ContainerEndpoint:   p.opts.ContainerEndpoint,
		WorkspaceVolume:     p.opts.WorkspaceVolume,
		WorkspaceVolumeRoot: p.opts.WorkspaceVolumeRoot,
		// The create request decides, not this service's configuration: a detached
		// workspace changes what the *control plane* can reach, and the control
		// plane is the only side that knows whether it is prepared for that.
		DetachedWorkspace: request.Detached,
	})
}

// api builds a provider that only talks to an existing container's API: no image
// (a stateless runner does not remember the one a create carried) and no CLI
// lookup (the checkout and worktree calls never spawn one).
func (p *DockerProvider) api(apiKey string) (*sandbox.Docker, error) {
	return sandbox.NewDockerAPI(sandbox.DockerConfig{
		Network:             p.opts.Network,
		DataRoot:            p.opts.DataRoot,
		APIPort:             p.apiPort(0),
		APIKey:              firstNonEmpty(apiKey, p.opts.APIKey),
		ContainerEndpoint:   p.opts.ContainerEndpoint,
		WorkspaceVolume:     p.opts.WorkspaceVolume,
		WorkspaceVolumeRoot: p.opts.WorkspaceVolumeRoot,
	})
}

func (p *DockerProvider) apiPort(requested int) int {
	if requested > 0 {
		return requested
	}
	return p.opts.APIPort
}

// record builds the wire record from the provider's answer. Endpoint comes from
// the provider rather than from a remembered port: the address is assigned at
// creation and survives a restart, and a copy kept in memory would be wrong after
// either.
//
// APIKey is copied from the create's answer and returned to the control plane over
// its bearer-protected internal API, once. The control plane is the only place the
// key can live: this service is stateless, so a later worktree request re-reads the
// key from the container's environment instead of remembering it. The alternative
// (the control plane generates the key and pushes it down on every create) puts the
// secret on the wire repeatedly and still leaves the runner guessing for a container
// it adopted, so generating where the container is created and reporting upward once
// is the smaller exposure.
func (p *DockerProvider) record(ctx context.Context, provider *sandbox.Docker, sb *contracts.Sandbox) (sandbox.RunnerRecord, error) {
	record := sandbox.RunnerRecord{
		ID:                sb.ID,
		State:             sb.State,
		RootDir:           sb.RootDir,
		ContainerPathRoot: sandbox.ContainerWorkspace,
		APIKey:            sb.SandboxAPIKey,
		Workspace:         sb.Workspace.Normalized(),
	}
	endpoint, err := provider.Endpoint(ctx, sb.ID)
	if err != nil {
		return sandbox.RunnerRecord{}, err
	}
	record.Endpoint = endpoint
	return record, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
