package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/store"
)

// sandboxKeys resolves the API key for one sandbox's own HTTP API.
//
// Two sources, in this order:
//
//  1. the key the runner (or the in-process docker provider) reported when the
//     sandbox was created, sealed in the sandboxes row — one key per container,
//     which is what makes a key leaked from one tenant useless against another;
//  2. the deployment-wide -sandbox-api-key override, for a deployment that wants
//     a fixed key while debugging, and for rows created before per-sandbox keys.
//
// If neither has an answer the call fails. It must not fall through to "no key":
// an unauthenticated request to a sandbox whose API happens to be open looks
// exactly like a working deployment until somebody else on the network finds it.
//
// The resolved key is never logged and never placed in a URL; it goes into the
// X-AIO-API-Key header of the client that needed it.
type sandboxKeys struct {
	store *store.Store
	// fixed is -sandbox-api-key: an optional override, never the default.
	fixed string
}

// For returns the key sandboxID's API requires.
func (k sandboxKeys) For(ctx context.Context, sandboxID string) (string, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return "", fmt.Errorf("sandbox: an API key was requested for an empty sandbox id")
	}
	if k.store != nil {
		key, err := k.store.SandboxAPIKey(ctx, sandboxID)
		switch {
		case err == nil && strings.TrimSpace(key) != "":
			return key, nil
		case err == nil, errors.Is(err, store.ErrNotFound):
			// The row has no key, or there is no row: the override is the only
			// remaining answer.
		default:
			return "", fmt.Errorf("sandbox %s: read its API key: %w", sandboxID, err)
		}
	}
	if fixed := strings.TrimSpace(k.fixed); fixed != "" {
		return fixed, nil
	}
	return "", fmt.Errorf("sandbox %s has no recorded API key (it was created before per-sandbox keys, or by a runner that did not report one); refusing to call its API unauthenticated — recreate the sandbox, or set -sandbox-api-key for a fixed key", sandboxID)
}
