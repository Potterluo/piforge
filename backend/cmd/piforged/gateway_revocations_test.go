package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/store"
)

// The stop hook revokes a session's gateway token for every stop the user or the
// platform causes — and deliberately not for the stop that happens while this
// process is shutting down, because a container placement's agent is expected to
// outlive the control plane (internal/agentsupervisor, stage 3). This pins both
// halves, because the second one is the one a later refactor would "tidy up".
func TestRevocationHookSkipsOnlyTheShutdownStop(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	signer, err := llmgw.NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	signer.UseRevocations(st)

	hook := &revocationHook{signer: signer}

	// A normal stop (pause, archive, delete, model switch, idle reap) revokes.
	hook.onStop("ses_pause")
	if n, err := st.GatewayRevocationCount(ctx); err != nil || n != 1 {
		t.Fatalf("after a normal stop: %d rows, %v; want 1, nil", n, err)
	}

	// The control plane starts shutting down; pi.Runtime.Close then "stops"
	// every live session, and those must not be persisted.
	hook.drain()
	hook.onStop("ses_surviving_agent")
	if n, err := st.GatewayRevocationCount(ctx); err != nil || n != 1 {
		t.Fatalf("a stop during shutdown wrote a revocation: %d rows, %v; want 1, nil", n, err)
	}
	// Nor did it bump the in-memory generation, which would be a lie the next
	// token mint would inherit if the process somehow kept running.
	if signer.Revoked("ses_surviving_agent") {
		t.Fatal("a stop during shutdown was recorded in the in-memory set")
	}
}
