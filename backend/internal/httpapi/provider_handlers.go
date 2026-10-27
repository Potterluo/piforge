package httpapi

import (
	"net/http"
	"sort"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// providersResponse is the merged provider view: admin-scope entries marked
// inherited plus the caller's own, shadowed by name (PRD §5.7.1).
type providersResponse struct {
	Providers []*contracts.Provider `json:"providers"`
}

type providerResponse struct {
	Provider *contracts.Provider `json:"provider"`
}

// handleListProviders merges inherited and custom providers.
//
// The merge is by name, and the user's entry wins: that is fastclaw's "single
// table + scope shadow" rule, which exists so "use a cheaper model" is one
// override rather than a copy of the whole config.
func (s *server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	inherited, err := s.deps.Store.InheritedProviders(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	own, err := s.deps.Store.ListProviders(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	merged := make(map[string]*contracts.Provider, len(inherited)+len(own))
	for _, p := range inherited {
		merged[p.Name] = p
	}
	for _, p := range own {
		merged[p.Name] = p
	}
	out := make([]*contracts.Provider, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, providersResponse{Providers: out})
}

// handleCreateProvider creates a private provider.
func (s *server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	var in struct {
		Name    string                `json:"name"`
		API     string                `json:"api"`
		BaseURL string                `json:"base_url"`
		Models  []contracts.ModelSpec `json:"models"`
		APIKey  string                `json:"api_key"`
		Enabled *bool                 `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	// KeyCipher is opaque to the store. Wrapping the key with the platform's
	// AES-GCM box belongs to the secret layer (PRD §5.7 FR-13.3); until that
	// box is injected here, the raw value is what gets persisted, so this is
	// the exact seam to change.
	p, err := s.deps.Store.CreateProvider(r.Context(), user.ID, store.ProviderInput{
		Name:      in.Name,
		API:       in.API,
		BaseURL:   in.BaseURL,
		Models:    in.Models,
		KeyCipher: []byte(in.APIKey),
		Enabled:   in.Enabled,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, providerResponse{Provider: p})
}

// handleDeleteProvider removes one of the caller's providers. Shared
// (inherited) providers are not deletable here, and an id owned by somebody
// else answers 404 just like a missing one.
func (s *server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.deps.Store.DeleteProvider(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}
