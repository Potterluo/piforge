package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

// sandboxFileBackend maps a host workspace path to the container that owns it.
//
// It is the hook that makes -sandbox-files remote work: the store knows which
// sandbox a worktree path belongs to, the docker provider knows the published
// endpoint and the inside-the-container path, and this type turns that pair into
// the one thing workspaceremote needs.
type sandboxFileBackend struct {
	provider *sandbox.Docker
	store    *store.Store
	apiKey   string

	mu    sync.Mutex
	cache map[string]cachedSandboxClient
}

// cachedSandboxClient keeps one client per sandbox. `docker port` is an exec per
// call, which is not something to do three times for one file preview; the
// endpoint only changes if the container is recreated, and the TTL bounds how
// long a stale one can be used after that.
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
	client, err := aio.New(aio.Config{
		BaseURL: endpoint,
		APIKey:  b.apiKey,
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
