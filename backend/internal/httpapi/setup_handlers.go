package httpapi

import (
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/bootstrap"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// setupResponse is the deterministic first-run state (FR-1.1 / QA-16).
//
// It exists because the frontend was already computing `hasUsableModel` from
// the provider list, and the two facts it could not know — "am I an admin" and
// "is there already a shared provider" — were being guessed from other
// responses.
type setupResponse struct {
	NeedsProvider bool `json:"needs_provider"`
	IsAdmin       bool `json:"is_admin"`
	// SharedProviderAvailable reports whether the deployment already has a
	// usable shared provider (enabled, keyed, at least one model), i.e. the one
	// every account inherits. When it is false and needs_provider is true, the
	// missing piece is deployment-wide.
	SharedProviderAvailable bool `json:"shared_provider_available"`
	// Bootstrap is the documented deployment prefill (name/base_url/model). It
	// has no key field on purpose: the startup environment owns the secret, the
	// UI only offers the non-secret parts.
	Bootstrap *bootstrap.Defaults `json:"bootstrap,omitempty"`
	// DefaultProvider is the name the deployment falls back to when a session
	// pins no model (`-default-provider` / PIFORGE_DEFAULT_PROVIDER).
	DefaultProvider string `json:"default_provider,omitempty"`
	// DefaultProviderMissing reports that this name does not resolve for the
	// signed-in account. The deployment came up with a default nobody created;
	// the first-run panel can then say so instead of letting the first send
	// discover it as a failure. Not fatal by design: an admin may add it later.
	DefaultProviderMissing bool `json:"default_provider_missing,omitempty"`
}

// handleSetup reports what a signed-in account needs to send its first message.
//
// It is a read of the same merged provider list the settings page shows, behind
// one boolean, so the home page and the composer can agree on "there is nothing
// to send to" without both re-implementing the usable-provider rule.
func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	providers, err := s.mergedProviders(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	needsProvider := true
	sharedUsable := false
	for _, provider := range providers {
		usable := providerUsable(provider)
		if usable {
			needsProvider = false
		}
		if usable && provider.Source == contracts.SourceInherited {
			sharedUsable = true
		}
	}

	resp := setupResponse{
		NeedsProvider:           needsProvider,
		IsAdmin:                 user.Role == contracts.RoleAdmin,
		SharedProviderAvailable: sharedUsable,
	}
	// The deployment default is what a session with no model_ref uses, so a name
	// that resolves to nothing is the reason the first send fails. Report it as
	// a fact about this account, not a guess.
	if def := strings.TrimSpace(s.deps.DefaultProvider); def != "" {
		resp.DefaultProvider = def
		if _, _, err := s.deps.Store.ResolveProvider(r.Context(), user.ID, def); err != nil {
			resp.DefaultProviderMissing = true
		}
	}
	if defaults := s.deps.SetupDefaults; !defaults.Empty() {
		resp.Bootstrap = &defaults
	}
	writeJSON(w, http.StatusOK, resp)
}

// providerUsable is the server-side twin of the frontend's hasUsableModel:
// a provider that is disabled, has no key or lists no models cannot answer a
// prompt, so counting it is how a send that can only fail looked configured.
func providerUsable(provider *contracts.Provider) bool {
	return provider != nil && provider.Enabled && provider.HasKey && len(provider.Models) > 0
}
