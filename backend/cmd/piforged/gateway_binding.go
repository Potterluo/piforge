package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/store"
)

// The wiring behind "a model gateway token only works from its own sandbox"
// (docs/EXECUTION-BOUNDARY.md §16.3).
//
// The rules live in internal/llmgw/binding.go and internal/store/sandbox_proof.go;
// this file is the two adapters that connect them to the store, plus the one
// decision that has to be made where the placement is known: which sandboxes a
// token can be bound to at all.

// sandboxProofSecrets answers the gateway's question "what is this sandbox's
// proof secret".
//
// It reads a row and never mints one: the gateway must not be the component that
// decides a secret exists, or a verification path that creates state would be one
// request away from a denial-of-service on the write pool. Minting happens at
// spawn (sandboxBindingFor).
type sandboxProofSecrets struct{ st *store.Store }

var _ llmgw.ProofSecrets = sandboxProofSecrets{}

func (p sandboxProofSecrets) SandboxProofSecret(ctx context.Context, sandboxID string) (string, error) {
	if p.st == nil {
		return "", nil
	}
	return p.st.SandboxProofSecret(ctx, sandboxID)
}

// sessionSandboxes answers "which sandbox is this session running in", for the
// MCP broker's half of the binding.
//
// A local placement answers "" — there is no container to bind to — and so does a
// session with no sandbox row. Anything else is a container name, which is what
// the resolver knows how to look up.
type sessionSandboxes struct{ st *store.Store }

var _ llmgw.SessionSandboxes = sessionSandboxes{}

func (s sessionSandboxes) SandboxOfSession(ctx context.Context, sessionID string) (string, error) {
	if s.st == nil {
		return "", nil
	}
	target, err := s.st.SessionSandboxFor(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if !isContainerProvider(target.Provider) {
		return "", nil
	}
	return target.SandboxID, nil
}

// sandboxBindingFor resolves the sandbox a session's next agent will run in, and
// that sandbox's proof secret.
//
// Empty sandbox id means "mint an unbound token": the local placement runs the
// agent on this host, so there is no other address to require and claiming one
// would lock the agent out of its own gateway.
func sandboxBindingFor(ctx context.Context, st *store.Store, sessionID string) (sandboxID, secret string, err error) {
	if st == nil || strings.TrimSpace(sessionID) == "" {
		return "", "", nil
	}
	target, terr := st.SessionSandboxFor(ctx, sessionID)
	if terr != nil {
		if errors.Is(terr, store.ErrNotFound) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("sandbox binding: %w", terr)
	}
	if !isContainerProvider(target.Provider) {
		return "", "", nil
	}
	secret, serr := st.EnsureSandboxProofSecret(ctx, target.SandboxID)
	if serr != nil {
		return "", "", fmt.Errorf("sandbox binding: proof secret of %s: %w", target.SandboxID, serr)
	}
	return target.SandboxID, secret, nil
}

// isContainerProvider reports whether a sandbox row names a container, and
// therefore whether its token can be bound to an address at all.
func isContainerProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "docker", "runner":
		return true
	default:
		return false
	}
}
