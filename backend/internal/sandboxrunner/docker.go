package sandboxrunner

import (
	"context"
	"fmt"
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
	// APIKey is the default SANDBOX_API_KEY for new containers, and the key used
	// to reach an existing container's API when a request does not carry one.
	APIKey string
	// ContainerEndpoint makes the provider report http://<container>:<APIPort> on
	// Network instead of a published loopback port. Set it when the control plane
	// is not a host process (see sandbox.DockerConfig.ContainerEndpoint).
	ContainerEndpoint bool
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
	return p.record(ctx, provider, sb)
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

// creator builds a provider that can create a container: image and CLI required.
func (p *DockerProvider) creator(request sandbox.RunnerRequest) (*sandbox.Docker, error) {
	image := firstNonEmpty(request.Image, p.opts.Image)
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("sandboxrunner: no image: the request carried none and the runner has no default (SANDBOX_IMAGE)")
	}
	return sandbox.NewDocker(sandbox.DockerConfig{
		Image:             image,
		Network:           firstNonEmpty(request.Network, p.opts.Network),
		CPUs:              firstNonEmpty(request.CPUs, p.opts.CPUs),
		Memory:            firstNonEmpty(request.Memory, p.opts.Memory),
		DataRoot:          p.opts.DataRoot,
		APIPort:           p.apiPort(request.APIPort),
		APIKey:            firstNonEmpty(request.APIKey, p.opts.APIKey),
		ContainerEndpoint: p.opts.ContainerEndpoint,
	})
}

// api builds a provider that only talks to an existing container's API: no image
// (a stateless runner does not remember the one a create carried) and no CLI
// lookup (the checkout and worktree calls never spawn one).
func (p *DockerProvider) api(apiKey string) (*sandbox.Docker, error) {
	return sandbox.NewDockerAPI(sandbox.DockerConfig{
		Network:           p.opts.Network,
		DataRoot:          p.opts.DataRoot,
		APIPort:           p.apiPort(0),
		APIKey:            firstNonEmpty(apiKey, p.opts.APIKey),
		ContainerEndpoint: p.opts.ContainerEndpoint,
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
func (p *DockerProvider) record(ctx context.Context, provider *sandbox.Docker, sb *contracts.Sandbox) (sandbox.RunnerRecord, error) {
	record := sandbox.RunnerRecord{
		ID:                sb.ID,
		State:             sb.State,
		RootDir:           sb.RootDir,
		ContainerPathRoot: sandbox.ContainerWorkspace,
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
