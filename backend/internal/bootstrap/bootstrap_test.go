package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/store"
)

func testStore(t *testing.T) (*store.Store, *contracts.User) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, err := st.CreateUser(context.Background(), store.NewUser{
		Email: "admin@bootstrap.test", DisplayName: "Admin",
		PasswordHash: "not-used-by-this-test", Role: contracts.RoleAdmin, Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return st, admin
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    Defaults
		wantErr bool
	}{
		{name: "empty", spec: "", want: Defaults{}},
		{name: "full", spec: "volc|https://ark.example/api/v3|doubao-seed", want: Defaults{
			Name: "volc", BaseURL: "https://ark.example/api/v3", Model: "doubao-seed",
		}},
		{name: "empty base url", spec: "volc||doubao-seed", want: Defaults{Name: "volc", Model: "doubao-seed"}},
		{name: "missing model", spec: "volc|https://ark.example/api/v3", wantErr: true},
		{name: "missing name", spec: "|https://ark.example/api/v3|m", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.spec, got, tc.want)
			}
		})
	}
}

// The deployment contract: one shared provider, created once, never replaced,
// and the key never appears in a log line.
func TestEnsureCreatesOnceAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	st, admin := testStore(t)
	box, err := crypto.New(bytes.Repeat([]byte{9}, crypto.KeySize))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	const key = "sk-bootstrap-super-secret"
	first, err := Parse("bootstrap-volc|https://ark.example/api/v3|doubao-seed-1-6")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	provider, created, err := Ensure(ctx, st, box, logger, first, key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Fatal("first Ensure did not create the provider")
	}
	if provider.Name != "bootstrap-volc" || provider.BaseURL != "https://ark.example/api/v3" {
		t.Fatalf("provider = %+v", provider)
	}
	if !provider.HasKey {
		t.Fatal("the bootstrap provider has no key")
	}
	if len(provider.Models) != 1 || provider.Models[0].ID != "doubao-seed-1-6" {
		t.Fatalf("models = %+v", provider.Models)
	}

	// Idempotent: a second boot with the same env changes nothing.
	again, createdAgain, err := Ensure(ctx, st, box, logger, first, key)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if createdAgain {
		t.Fatal("second Ensure created another provider")
	}
	if again.ID != provider.ID {
		t.Fatalf("second Ensure returned %q, want %q", again.ID, provider.ID)
	}

	// Never overwrite: a changed environment (new model, new key, new endpoint)
	// must leave the operator's existing provider alone.
	changed, err := Parse("bootstrap-volc|https://other.example/v1|other-model")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	third, createdThird, err := Ensure(ctx, st, box, logger, changed, "sk-a-different-key")
	if err != nil {
		t.Fatalf("third Ensure: %v", err)
	}
	if createdThird {
		t.Fatal("third Ensure overwrote the existing provider")
	}
	if third.ID != provider.ID || third.BaseURL != "https://ark.example/api/v3" {
		t.Fatalf("existing provider was replaced: %+v", third)
	}
	if len(third.Models) != 1 || third.Models[0].ID != "doubao-seed-1-6" {
		t.Fatalf("existing model list was replaced: %+v", third.Models)
	}
	if !third.HasKey {
		t.Fatal("existing provider lost its key")
	}

	admins, err := st.AdminListProviders(ctx, admin.ID)
	if err != nil {
		t.Fatalf("AdminListProviders: %v", err)
	}
	if len(admins) != 1 {
		t.Fatalf("shared providers = %d, want 1", len(admins))
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "created the shared model provider") {
		t.Fatalf("creation was not logged:\n%s", logged)
	}
	if strings.Contains(logged, key) || strings.Contains(logged, "sk-a-different-key") {
		t.Fatalf("the provider key reached the log:\n%s", logged)
	}
	if strings.Contains(logged, "bootstrap-volc") == false {
		t.Fatalf("the log does not name the provider:\n%s", logged)
	}
}

func TestEnsureIsANoOpWithoutConfiguration(t *testing.T) {
	ctx := context.Background()
	st, _ := testStore(t)
	provider, created, err := Ensure(ctx, st, nil, nil, Defaults{}, "ignored")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if provider != nil || created {
		t.Fatalf("empty defaults created %+v", provider)
	}
}

// A fresh deployment's seed creates `shared-anthropic` with no key. That row
// cannot answer a prompt, so it must not count as "a provider already exists"
// — the bootstrap variable exists precisely to make that deployment usable.
func TestEnsureCreatesAlongsideAKeylessPlaceholder(t *testing.T) {
	ctx := context.Background()
	st, admin := testStore(t)
	box, err := crypto.New(bytes.Repeat([]byte{3}, crypto.KeySize))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	if _, err := st.UpsertAdminProvider(ctx, admin.ID, store.ProviderInput{
		Name: "shared-anthropic", API: "anthropic-messages",
		Models: []contracts.ModelSpec{{ID: "claude-sonnet-4-5"}},
	}); err != nil {
		t.Fatalf("seed placeholder: %v", err)
	}

	defaults, err := Parse("volc|https://ark.example/api/v3|doubao-seed-1-6")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	provider, created, err := Ensure(ctx, st, box, nil, defaults, "sk-volc-bootstrap")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created || provider.Name != "volc" {
		t.Fatalf("keyless placeholder blocked bootstrap: created=%v provider=%+v", created, provider)
	}

	// Now that a usable shared provider exists, another bootstrap provider is
	// not added: the deployment is no longer dead on arrival.
	other, err := Parse("other|https://other.example/v1|other-model")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kept, createdOther, err := Ensure(ctx, st, box, nil, other, "sk-other")
	if err != nil {
		t.Fatalf("Ensure(other): %v", err)
	}
	if createdOther || kept.Name != "volc" {
		t.Fatalf("a usable shared provider did not stop a second bootstrap: created=%v provider=%+v", createdOther, kept)
	}

	// Same name is never overwritten, even when the environment changes.
	changed, err := Parse("volc|https://changed.example/v1|changed-model")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	same, createdSame, err := Ensure(ctx, st, box, nil, changed, "sk-changed")
	if err != nil {
		t.Fatalf("Ensure(changed): %v", err)
	}
	if createdSame || same.ID != provider.ID || same.BaseURL != "https://ark.example/api/v3" {
		t.Fatalf("existing bootstrap provider was replaced: created=%v provider=%+v", createdSame, same)
	}
	if len(same.Models) != 1 || same.Models[0].ID != "doubao-seed-1-6" {
		t.Fatalf("existing model list was replaced: %+v", same.Models)
	}
}
