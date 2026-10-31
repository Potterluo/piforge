package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/bootstrap"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

type setupBody struct {
	NeedsProvider           bool                `json:"needs_provider"`
	IsAdmin                 bool                `json:"is_admin"`
	SharedProviderAvailable bool                `json:"shared_provider_available"`
	Bootstrap               *bootstrap.Defaults `json:"bootstrap"`
	DefaultProvider         string              `json:"default_provider"`
	DefaultProviderMissing  bool                `json:"default_provider_missing"`
}

func (e *testEnv) setup(c *creds) setupBody {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/v1/setup", nil, c, "")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET /v1/setup: status %d body %s", rec.Code, rec.Body.String())
	}
	var body setupBody
	e.mustJSON(rec, &body)
	return body
}

// A fresh account has no provider anyone can send to. /v1/setup is the single
// honest answer to "is this account configured", and it must track the same
// usable-provider rule the frontend applies: enabled, keyed, at least one model.
func TestSetupReportsFirstRunState(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("setup-admin@example.test", "setup-admin-pw", contracts.RoleAdmin)
	env.mkUser("setup-user@example.test", "setup-user-pw", contracts.RoleUser)

	user := env.login("setup-user@example.test", "setup-user-pw")
	admin := env.login("setup-admin@example.test", "setup-admin-pw")

	if got := env.setup(user); got.NeedsProvider != true || got.IsAdmin || got.SharedProviderAvailable {
		t.Fatalf("fresh user setup = %+v, want needsProvider", got)
	}
	if got := env.setup(admin); !got.NeedsProvider || !got.IsAdmin || got.SharedProviderAvailable {
		t.Fatalf("fresh admin setup = %+v, want needsProvider+admin", got)
	}

	// A provider without a key is not usable: this is the seeded shared
	// "shared-anthropic" shape on a fresh deployment, and counting it was the
	// original bug (a send that could only return 400 no_model).
	rec := env.do(http.MethodPost, "/v1/providers", map[string]any{
		"name": "keyless", "api": "openai-compat",
		"models": []map[string]any{{"id": "m1"}},
	}, user, user.csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create keyless provider: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := env.setup(user); !got.NeedsProvider {
		t.Fatalf("a keyless provider was counted as usable: %+v", got)
	}

	// A disabled provider is not usable either.
	disabled := false
	rec = env.do(http.MethodPost, "/v1/providers", map[string]any{
		"name": "disabled", "api": "openai-compat",
		"models": []map[string]any{{"id": "m1"}}, "api_key": "sk-disabled",
		"enabled": disabled,
	}, user, user.csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create disabled provider: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := env.setup(user); !got.NeedsProvider {
		t.Fatalf("a disabled provider was counted as usable: %+v", got)
	}

	// A real personal provider makes this account ready, but the deployment is
	// still missing the shared one every other account inherits.
	rec = env.do(http.MethodPost, "/v1/providers", map[string]any{
		"name": "mine", "api": "openai-compat", "base_url": "https://mine.example/v1",
		"models": []map[string]any{{"id": "m1"}}, "api_key": "sk-mine",
	}, user, user.csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create usable provider: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := env.setup(user); got.NeedsProvider || got.SharedProviderAvailable {
		t.Fatalf("user with a personal provider = %+v, want ready and no shared provider", got)
	}

	// An admin's shared provider flips both facts for every account.
	rec = env.do(http.MethodPost, "/v1/admin/providers", map[string]any{
		"name": "shared", "api": "openai-compat", "base_url": "https://shared.example/v1",
		"models": []map[string]any{{"id": "shared-model"}}, "api_key": "sk-shared",
	}, admin, admin.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("create shared provider: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := env.setup(user); got.NeedsProvider || !got.SharedProviderAvailable {
		t.Fatalf("after a shared provider = %+v, want ready and shared available", got)
	}

	// Another fresh account inherits it and is ready without doing anything.
	env.mkUser("late-user@example.test", "late-user-pw", contracts.RoleUser)
	late := env.login("late-user@example.test", "late-user-pw")
	if got := env.setup(late); got.NeedsProvider || !got.SharedProviderAvailable {
		t.Fatalf("a new account did not inherit the shared provider: %+v", got)
	}
}

// TestSetupReportsAMissingDefaultProvider: the acceptance deployment started
// with PIFORGE_DEFAULT_PROVIDER=volc while no volc provider existed. Nothing
// complained at boot and the first send failed with 400 no_model; the first-run
// panel needs the fact, not a guess made from the provider list.
func TestSetupReportsAMissingDefaultProvider(t *testing.T) {
	env := newTestEnv(t)
	admin := env.mkUser("setup-default@example.test", "setup-default-pw", contracts.RoleAdmin)
	c := env.login("setup-default@example.test", "setup-default-pw")

	env.deps.DefaultProvider = "volc"
	env.rebuild()

	got := env.setup(c)
	if got.DefaultProvider != "volc" {
		t.Fatalf("default_provider = %q, want volc", got.DefaultProvider)
	}
	if !got.DefaultProviderMissing {
		t.Fatalf("a default provider that does not exist was reported as present: %+v", got)
	}

	// Adding the provider the deployment names is the documented fix, and the
	// field must follow it without a restart.
	env.ensureProvider(admin.ID, "volc", "deepseek-v4-flash")
	got = env.setup(c)
	if got.DefaultProviderMissing {
		t.Fatalf("default provider still reported missing after it was added: %+v", got)
	}

	// A disabled provider is not a usable default either: the name exists but
	// nothing can send to it, so calling it present would repeat the bug.
	providers, err := env.store.ListProviders(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	var volcID string
	for _, provider := range providers {
		if provider.Name == "volc" {
			volcID = provider.ID
		}
	}
	disabled := false
	if _, err := env.store.UpdateProvider(context.Background(), admin.ID, volcID, store.ProviderPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if got = env.setup(c); !got.DefaultProviderMissing {
		t.Fatalf("a disabled default provider counted as present: %+v", got)
	}
}

// The bootstrap prefill is non-secret by construction, and only present when
// the deployment set the variable.
func TestSetupExposesBootstrapPrefillWithoutSecrets(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("setup-admin@example.test", "setup-admin-pw", contracts.RoleAdmin)
	admin := env.login("setup-admin@example.test", "setup-admin-pw")

	if got := env.setup(admin); got.Bootstrap != nil {
		t.Fatalf("bootstrap defaults present without configuration: %+v", got.Bootstrap)
	}

	env.deps.SetupDefaults = bootstrap.Defaults{
		Name:    "volc",
		BaseURL: "https://ark.example/api/v3",
		Model:   "doubao-seed-1-6",
	}
	env.rebuild()

	rec := env.do(http.MethodGet, "/v1/setup", nil, admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/setup: status %d", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "api_key") || strings.Contains(raw, "key") {
		t.Fatalf("setup response carries key material: %s", raw)
	}
	var body setupBody
	env.mustJSON(rec, &body)
	if body.Bootstrap == nil {
		t.Fatal("bootstrap defaults missing")
	}
	if body.Bootstrap.Name != "volc" ||
		body.Bootstrap.BaseURL != "https://ark.example/api/v3" ||
		body.Bootstrap.Model != "doubao-seed-1-6" {
		t.Fatalf("bootstrap defaults = %+v", body.Bootstrap)
	}
}
