package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The per-sandbox proof secret is the durable half of "a model token only works
// from its own sandbox" (docs/EXECUTION-BOUNDARY.md §16.3): the gateway has to
// recognise a proof the agent computed at spawn time, possibly before a restart,
// so the secret is a row and not a process-local value.
func TestSandboxProofSecretIsPerSandboxSealedAndStable(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "dev@piforge.local", contracts.RoleUser)
	first := mkProject(t, st, user.ID, "first")
	second := mkProject(t, st, user.ID, "second")

	sbA, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: first.ID})
	if err != nil {
		t.Fatalf("CreateSandbox(A): %v", err)
	}
	sbB, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: second.ID})
	if err != nil {
		t.Fatalf("CreateSandbox(B): %v", err)
	}

	// A sandbox created before this column existed has no secret: empty, not an
	// error, so the first spawn can mint one.
	if secret, err := st.SandboxProofSecret(ctx, sbA.ID); err != nil || secret != "" {
		t.Fatalf("SandboxProofSecret(fresh row) = %q, %v; want empty", secret, err)
	}

	secretA, err := st.EnsureSandboxProofSecret(ctx, sbA.ID)
	if err != nil {
		t.Fatalf("EnsureSandboxProofSecret(A): %v", err)
	}
	if len(secretA) != 64 {
		t.Fatalf("secret length = %d, want 64 hex characters", len(secretA))
	}
	// Idempotent: the agent launcher calls this on every spawn, and a second value
	// would silently invalidate every proof minted before it.
	again, err := st.EnsureSandboxProofSecret(ctx, sbA.ID)
	if err != nil || again != secretA {
		t.Fatalf("EnsureSandboxProofSecret(A) twice = %q, %v; want the same secret", again, err)
	}
	secretB, err := st.EnsureSandboxProofSecret(ctx, sbB.ID)
	if err != nil {
		t.Fatalf("EnsureSandboxProofSecret(B): %v", err)
	}
	if secretB == secretA {
		t.Fatal("two sandboxes share one proof secret, so one sandbox could forge the other's proof")
	}

	// Sealed: the database file does not contain the plaintext.
	var blob []byte
	if err := st.r.QueryRowContext(ctx,
		`SELECT proof_cipher FROM sandboxes WHERE id = ?`, sbA.ID).Scan(&blob); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("the secret was not stored")
	}
	if strings.Contains(string(blob), secretA) {
		t.Fatal("a sandbox stores its proof secret in the clear")
	}

	// It survives a restart: a control-plane restart must not orphan the proof of
	// an agent that is still running (stage 3's reattach).
	path := st.path
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got, err := reopened.SandboxProofSecret(ctx, sbA.ID); err != nil || got != secretA {
		t.Fatalf("after a reopen: %q, %v; want the same secret", got, err)
	}

	if _, err := reopened.SandboxProofSecret(ctx, "sbx_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SandboxProofSecret(unknown) = %v, want ErrNotFound", err)
	}
}
