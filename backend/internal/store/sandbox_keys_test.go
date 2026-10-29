package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The per-sandbox API key is a secret with a short life and a wide blast radius:
// it opens one tenant's shell, files and browser. It is stored sealed, it is read
// back only through SandboxAPIKey, and a sandbox row read any other way never
// carries it.
func TestSandboxAPIKeyIsPerSandboxAndSealed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "dev@piforge.local", contracts.RoleUser)
	first := mkProject(t, st, user.ID, "first")
	second := mkProject(t, st, user.ID, "second")

	const keyA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const keyB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	sbA, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: first.ID, APIKey: keyA})
	if err != nil {
		t.Fatalf("CreateSandbox(A): %v", err)
	}
	sbB, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: second.ID, APIKey: keyB})
	if err != nil {
		t.Fatalf("CreateSandbox(B): %v", err)
	}

	// Two sandboxes, two keys, each read back exactly.
	gotA, err := st.SandboxAPIKey(ctx, sbA.ID)
	if err != nil || gotA != keyA {
		t.Fatalf("SandboxAPIKey(A) = %q, %v", gotA, err)
	}
	gotB, err := st.SandboxAPIKey(ctx, sbB.ID)
	if err != nil || gotB != keyB {
		t.Fatalf("SandboxAPIKey(B) = %q, %v", gotB, err)
	}
	if gotA == gotB {
		t.Fatal("two sandboxes share one key")
	}

	// The plaintext is not in the database file.
	for _, row := range []struct {
		id  string
		key string
	}{{sbA.ID, keyA}, {sbB.ID, keyB}} {
		var blob []byte
		if err := st.r.QueryRowContext(ctx,
			`SELECT api_key_cipher FROM sandboxes WHERE id = ?`, row.id).Scan(&blob); err != nil {
			t.Fatalf("read ciphertext: %v", err)
		}
		if len(blob) == 0 {
			t.Fatalf("sandbox %s stored no key", row.id)
		}
		if strings.Contains(string(blob), row.key) {
			t.Fatalf("sandbox %s stores its API key in the clear", row.id)
		}
	}

	// A sandbox read through the normal accessors does not carry the key, and does
	// not serialize it either: the row shape is what the API hands out.
	for _, sb := range []*contracts.Sandbox{sbA, sbB} {
		read, err := st.Sandbox(ctx, user.ID, sb.ID)
		if err != nil {
			t.Fatalf("Sandbox(%s): %v", sb.ID, err)
		}
		if read.SandboxAPIKey != "" {
			t.Fatalf("Sandbox(%s) carried the API key", sb.ID)
		}
		raw, err := json.Marshal(read)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), keyA) || strings.Contains(string(raw), keyB) {
			t.Fatalf("a sandbox row serialized its API key: %s", raw)
		}
	}

	// A sandbox created before per-sandbox keys has none, and that is reported as
	// an empty key rather than an error: the caller decides between the fixed
	// -sandbox-api-key override and a clear failure.
	third := mkProject(t, st, user.ID, "third")
	sbC, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: third.ID})
	if err != nil {
		t.Fatalf("CreateSandbox(C): %v", err)
	}
	if key, err := st.SandboxAPIKey(ctx, sbC.ID); err != nil || key != "" {
		t.Fatalf("SandboxAPIKey(no key) = %q, %v", key, err)
	}

	if _, err := st.SandboxAPIKey(ctx, "sbx_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SandboxAPIKey(unknown) = %v, want ErrNotFound", err)
	}
}
