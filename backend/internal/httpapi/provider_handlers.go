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

	// The key is sealed before it ever reaches the database (PRD §5.7 FR-13.3).
	// A deployment without a secret box refuses the write rather than storing a
	// credential in the clear — silently downgrading that guarantee is exactly
	// the failure mode nobody notices until a dump leaks.
	var cipherText []byte
	if in.APIKey != "" {
		if s.deps.Secrets == nil {
			s.fail(w, r, apiErr(contracts.ErrInternal, "secret storage is not configured on this server"))
			return
		}
		sealed, err := s.deps.Secrets.SealString(in.APIKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		cipherText = sealed
	}

	p, err := s.deps.Store.CreateProvider(r.Context(), user.ID, store.ProviderInput{
		Name:      in.Name,
		API:       in.API,
		BaseURL:   in.BaseURL,
		Models:    in.Models,
		KeyCipher: cipherText,
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

// handleUpdateProvider patches one of the caller's providers.
//
// Patching is what makes model management usable: a typo in a model id, a
// rotated key or a changed endpoint are all edits to an existing provider, and
// delete-and-recreate would discard the key that was typed once.
func (s *server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	var in struct {
		API      *string               `json:"api"`
		BaseURL  *string               `json:"base_url"`
		Models   []contracts.ModelSpec `json:"models"`
		APIKey   string                `json:"api_key"`
		ClearKey bool                  `json:"clear_key"`
		Enabled  *bool                 `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	patch := store.ProviderPatch{
		API:      in.API,
		BaseURL:  in.BaseURL,
		Models:   in.Models,
		ClearKey: in.ClearKey,
		Enabled:  in.Enabled,
	}
	if in.APIKey != "" {
		if s.deps.Secrets == nil {
			s.fail(w, r, apiErr(contracts.ErrInternal, "secret storage is not configured on this server"))
			return
		}
		sealed, err := s.deps.Secrets.SealString(in.APIKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		patch.KeyCipher = sealed
	}

	p, err := s.deps.Store.UpdateProvider(r.Context(), user.ID, pathID(r), patch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, providerResponse{Provider: p})
}

// Shared providers are the deployment's own model accounts (PRD FR-16.2): an
// admin configures one, every user inherits it, and a user's own provider of the
// same name shadows it. These three handlers are the admin half.

func (s *server) handleAdminListProviders(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	providers, err := s.deps.Store.AdminListProviders(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if providers == nil {
		providers = []*contracts.Provider{}
	}
	writeJSON(w, http.StatusOK, providersResponse{Providers: providers})
}

func (s *server) handleAdminUpsertProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}

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

	var cipherText []byte
	if in.APIKey != "" {
		if s.deps.Secrets == nil {
			s.fail(w, r, apiErr(contracts.ErrInternal, "secret storage is not configured on this server"))
			return
		}
		sealed, err := s.deps.Secrets.SealString(in.APIKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		cipherText = sealed
	}

	provider, err := s.deps.Store.UpsertAdminProvider(r.Context(), user.ID, store.ProviderInput{
		Name:      in.Name,
		API:       in.API,
		BaseURL:   in.BaseURL,
		Models:    in.Models,
		KeyCipher: cipherText,
		Enabled:   in.Enabled,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, providerResponse{Provider: provider})
}

func (s *server) handleAdminDeleteProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.deps.Store.DeleteAdminProvider(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}
