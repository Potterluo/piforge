package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Every sandbox API call the control plane makes goes through this resolver. The
// three cases it has to get right are the migration's compatibility promise:
// a per-sandbox key is used; a row without one falls back to -sandbox-api-key; and
// with neither, the call fails instead of going unauthenticated.
func TestSandboxKeysResolvePerSandboxFirst(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{
		Email: "dev@piforge.local", DisplayName: "dev", PasswordHash: "hash", Role: contracts.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "repo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	const perSandbox = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"
	sandbox, err := st.CreateSandbox(ctx, user.ID, store.NewSandbox{ProjectID: project.ID, APIKey: perSandbox})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	legacyProject, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "legacy", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject(legacy): %v", err)
	}
	legacy, err := st.CreateSandbox(ctx, user.ID, store.NewSandbox{ProjectID: legacyProject.ID})
	if err != nil {
		t.Fatalf("CreateSandbox(legacy): %v", err)
	}

	t.Run("the recorded key wins, even when a fixed override is configured", func(t *testing.T) {
		keys := sandboxKeys{store: st, fixed: "fixed-debug-key"}
		got, err := keys.For(ctx, sandbox.ID)
		if err != nil || got != perSandbox {
			t.Fatalf("For(sandbox) = %q, %v; want the recorded per-sandbox key", got, err)
		}
	})

	t.Run("a row without a key falls back to -sandbox-api-key", func(t *testing.T) {
		keys := sandboxKeys{store: st, fixed: "fixed-debug-key"}
		got, err := keys.For(ctx, legacy.ID)
		if err != nil || got != "fixed-debug-key" {
			t.Fatalf("For(legacy) = %q, %v; want the fixed override", got, err)
		}
	})

	t.Run("with no key and no override the call fails instead of going unauthenticated", func(t *testing.T) {
		keys := sandboxKeys{store: st}
		_, err := keys.For(ctx, legacy.ID)
		if err == nil {
			t.Fatal("a sandbox with no key was called unauthenticated")
		}
		for _, want := range []string{legacy.ID, "-sandbox-api-key", "unauthenticated"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want it to mention %q", err, want)
			}
		}
		if strings.Contains(err.Error(), perSandbox) {
			t.Fatalf("the error leaked a key: %q", err)
		}
	})

	t.Run("an unknown sandbox is the same clear failure", func(t *testing.T) {
		keys := sandboxKeys{store: st}
		_, err := keys.For(ctx, "piforge-doesnotexist")
		if err == nil || !strings.Contains(err.Error(), "unauthenticated") {
			t.Fatalf("For(unknown) = %v", err)
		}
		if _, err := keys.For(ctx, "  "); err == nil {
			t.Fatal("an empty sandbox id was accepted")
		}
	})

	t.Run("the reader refuses a broken store instead of masking it", func(t *testing.T) {
		closed, err := store.Open(ctx, filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		_ = closed.Close()
		keys := sandboxKeys{store: closed, fixed: "fixed-debug-key"}
		_, err = keys.For(ctx, sandbox.ID)
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("For with a closed store = %v, want the read error rather than a fallback", err)
		}
	})
}
