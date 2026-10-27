package store

import (
	"context"
	"errors"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// TestResolveProviderShadowing pins the inheritance rule the model gateway
// depends on: a user provider shadows a shared one with the same name, and a
// disabled provider is not resolvable at all.
func TestResolveProviderShadowing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	admin := mkUser(t, s, "admin@example.com", contracts.RoleAdmin)
	dev := mkUser(t, s, "dev@example.com", contracts.RoleUser)

	// A shared provider everyone inherits.
	if _, err := s.UpsertAdminProvider(ctx, admin.ID, ProviderInput{
		Name:      "anthropic",
		API:       "anthropic-messages",
		BaseURL:   "https://shared.example",
		KeyCipher: []byte("shared-sealed"),
		Models:    []contracts.ModelSpec{{ID: "claude-sonnet-4-5"}},
	}); err != nil {
		t.Fatalf("UpsertAdminProvider: %v", err)
	}

	// Before shadowing, the user resolves to the shared row.
	provider, sealed, err := s.ResolveProvider(ctx, dev.ID, "anthropic")
	if err != nil {
		t.Fatalf("ResolveProvider (inherited): %v", err)
	}
	if provider.Source != contracts.SourceInherited {
		t.Fatalf("source = %q, want inherited", provider.Source)
	}
	if string(sealed) != "shared-sealed" {
		t.Fatalf("sealed = %q", sealed)
	}
	if provider.BaseURL != "https://shared.example" {
		t.Fatalf("base URL = %q", provider.BaseURL)
	}

	// A personal provider with the same name must win.
	if _, err := s.CreateProvider(ctx, dev.ID, ProviderInput{
		Name:      "anthropic",
		API:       "anthropic-messages",
		BaseURL:   "https://personal.example",
		KeyCipher: []byte("personal-sealed"),
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	provider, sealed, err = s.ResolveProvider(ctx, dev.ID, "anthropic")
	if err != nil {
		t.Fatalf("ResolveProvider (shadowed): %v", err)
	}
	if provider.Source != contracts.SourceCustom {
		t.Fatalf("source = %q, want custom after shadowing", provider.Source)
	}
	if string(sealed) != "personal-sealed" {
		t.Fatalf("sealed = %q, want the personal key", sealed)
	}

	// Another user still sees the shared one: shadowing is per account.
	other := mkUser(t, s, "other@example.com", contracts.RoleUser)
	provider, _, err = s.ResolveProvider(ctx, other.ID, "anthropic")
	if err != nil {
		t.Fatalf("ResolveProvider (other user): %v", err)
	}
	if provider.Source != contracts.SourceInherited {
		t.Fatalf("another user lost the shared provider: %q", provider.Source)
	}
}

func TestResolveProviderRejectsUnknownAndDisabled(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dev := mkUser(t, s, "dev@example.com", contracts.RoleUser)

	if _, _, err := s.ResolveProvider(ctx, dev.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown provider: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.ResolveProvider(ctx, dev.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: err = %v, want ErrInvalid", err)
	}

	disabled := false
	if _, err := s.CreateProvider(ctx, dev.ID, ProviderInput{
		Name: "off", API: "openai-compat", Enabled: &disabled,
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, _, err := s.ResolveProvider(ctx, dev.ID, "off"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled provider resolved: err = %v", err)
	}
}

func TestProviderKeyCipherIsRoundTrippedVerbatim(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dev := mkUser(t, s, "dev@example.com", contracts.RoleUser)

	sealed := []byte{0x00, 0xff, 0x10, 0x20} // deliberately not valid UTF-8
	if _, err := s.CreateProvider(ctx, dev.ID, ProviderInput{
		Name: "k", API: "openai-compat", KeyCipher: sealed,
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	_, got, err := s.ResolveProvider(ctx, dev.ID, "k")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if string(got) != string(sealed) {
		t.Fatalf("sealed key = %v, want %v", got, sealed)
	}
}
