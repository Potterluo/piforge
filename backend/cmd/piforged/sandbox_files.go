package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

// sandboxFileBackend maps a host workspace path to the container that owns it.
//
// It is the hook that makes -sandbox-files remote work: the store knows which
// sandbox a worktree path belongs to, the provider knows the sandbox's endpoint
// and the inside-the-container path, and this type turns that pair into the one
// thing workspaceremote needs. The provider is an interface because a container
// sandbox may be owned by this process (-sandbox docker) or by a runner service
// (-sandbox runner); the panel does not care which.
type sandboxFileBackend struct {
	provider sandbox.Remote
	store    *store.Store
	// keys resolves the key of the sandbox that owns the worktree, per call: the
	// panel must present the same key the container was created with, and one
	// deployment-wide value would be wrong for every sandbox but one.
	keys sandboxKeys

	mu    sync.Mutex
	cache map[string]cachedSandboxClient
}

// cachedSandboxClient keeps one client per sandbox. Resolving the endpoint is a
// round trip to whatever owns the container (`docker port`, or an HTTP call to
// the runner), which is not something to do three times for one file preview; the
// endpoint only changes if the container is recreated, and the TTL bounds how long
// a stale one can be used after that.
type cachedSandboxClient struct {
	client *aio.Client
	at     time.Time
}

const sandboxClientTTL = 30 * time.Second

// Target implements workspaceremote.Backend.
func (b *sandboxFileBackend) Target(ctx context.Context, hostPath string) (workspaceremote.Target, error) {
	worktree, err := b.store.WorktreeByPath(ctx, hostPath)
	if err != nil {
		return workspaceremote.Target{}, fmt.Errorf("no sandbox owns %q: %w", hostPath, err)
	}
	containerPath, err := b.provider.ContainerPath(hostPath)
	if err != nil {
		return workspaceremote.Target{}, err
	}
	client, err := b.client(ctx, worktree.SandboxID)
	if err != nil {
		return workspaceremote.Target{}, err
	}
	return workspaceremote.Target{Client: client, Root: containerPath}, nil
}

func (b *sandboxFileBackend) client(ctx context.Context, sandboxID string) (*aio.Client, error) {
	now := time.Now()
	b.mu.Lock()
	if cached, ok := b.cache[sandboxID]; ok && now.Sub(cached.at) < sandboxClientTTL {
		b.mu.Unlock()
		return cached.client, nil
	}
	b.mu.Unlock()
	endpoint, err := b.provider.Endpoint(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s has no reachable endpoint: %w", sandboxID, err)
	}
	apiKey, err := b.keys.For(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	client, err := aio.New(aio.Config{
		BaseURL: endpoint,
		APIKey:  apiKey,
		Root:    "/workspace",
	})
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.cache == nil {
		b.cache = make(map[string]cachedSandboxClient)
	}
	b.cache[sandboxID] = cachedSandboxClient{client: client, at: now}
	b.mu.Unlock()
	return client, nil
}

// files opens one sandbox's workspace as the filesystem materialize writes into.
//
// It is the provisioner's seam for a workspace that exists only inside the
// container (stage 2 of docs/EXECUTION-BOUNDARY.md): the same client the file
// panel uses, the same key, the same container root — because the projection and
// the panel must never disagree about which tree they are describing.
//
// The root is the sandbox's workspace as the container names it; ContainerPath
// answers that for a detached sandbox and would refuse a host path outside a
// project, which is the guard that keeps a mis-resolved sandbox from writing into
// somebody else's tree.
func (b *sandboxFileBackend) files(ctx context.Context, sb *contracts.Sandbox) (materialize.FileSystem, error) {
	if sb == nil {
		return nil, fmt.Errorf("materialize: no sandbox")
	}
	root, err := b.provider.ContainerPath(sb.RootDir)
	if err != nil {
		return nil, fmt.Errorf("materialize: workspace of sandbox %s: %w", sb.ID, err)
	}
	client, err := b.client(ctx, sb.ID)
	if err != nil {
		return nil, fmt.Errorf("materialize: sandbox %s: %w", sb.ID, err)
	}
	return materialize.NewSandboxFS(materialize.SandboxConfig{Client: client, Root: root})
}
