package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// A deployment that names a provider nobody created must say so at boot, with
// the consequence. The acceptance pass started with PIFORGE_DEFAULT_PROVIDER=volc
// and no volc provider; nothing complained until the first send failed.
func TestDefaultProviderWarning(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "default-provider.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	warning, err := defaultProviderWarning(ctx, st, "volc")
	if err != nil {
		t.Fatalf("defaultProviderWarning: %v", err)
	}
	if warning == "" {
		t.Fatal("a missing default provider produced no warning")
	}
	for _, want := range []string{"volc", "does not exist", "no_model"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning %q does not mention %q", warning, want)
		}
	}

	// The provider being added later (the recovery the warning points at) makes
	// the warning go away; nothing about the check is cached.
	admin, err := st.CreateUser(ctx, store.NewUser{
		Email: "boot-admin@example.test", DisplayName: "boot", PasswordHash: "x",
		Role: contracts.RoleAdmin, Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := st.UpsertAdminProvider(ctx, admin.ID, store.ProviderInput{
		Name:      "volc",
		API:       "openai-compat",
		BaseURL:   "https://ark.example/api/v3",
		Models:    []contracts.ModelSpec{{ID: "deepseek-v4-flash"}},
		KeyCipher: []byte("sealed"),
	}); err != nil {
		t.Fatalf("UpsertAdminProvider: %v", err)
	}
	warning, err = defaultProviderWarning(ctx, st, "volc")
	if err != nil {
		t.Fatalf("defaultProviderWarning after add: %v", err)
	}
	if warning != "" {
		t.Fatalf("a provider that exists still warns: %s", warning)
	}

	// An empty name means "the deployment set nothing", which is not a
	// misconfiguration this check can judge.
	if warning, err := defaultProviderWarning(ctx, st, "  "); err != nil || warning != "" {
		t.Fatalf("empty name = (%q, %v), want no warning", warning, err)
	}
}
