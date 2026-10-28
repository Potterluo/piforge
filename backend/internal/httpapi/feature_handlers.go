package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Feature flags (PRD FR-16.7).
//
// Two rules make them worth having rather than decorative: the server enforces
// them on the endpoints they describe, and the client asks the server instead of
// guessing. A flag that only hides a button is a flag that lies.

// handleFeatures returns the caller's effective flags.
func (s *server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	flags, err := s.deps.Store.Features(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": flags})
}

// handleAdminUserFeatures reads one user's flags.
func (s *server) handleAdminUserFeatures(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	target := pathID(r)
	flags, err := s.deps.Store.Features(r.Context(), target)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": flags})
}

// handleAdminSetUserFeature writes one flag for one user.
func (s *server) handleAdminSetUserFeature(w http.ResponseWriter, r *http.Request) {
	admin := auth.MustUser(r.Context())
	var in struct {
		Flag    string `json:"flag"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Enabled == nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "enabled is required"))
		return
	}
	if !store.KnownFeature(in.Flag) {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "unknown feature %q", in.Flag))
		return
	}
	if err := s.deps.Store.SetFeature(r.Context(), admin.ID, pathID(r), in.Flag, *in.Enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

// featureDenied answers 403 with a message that says which switch is off.
func (s *server) featureDenied(w http.ResponseWriter, r *http.Request, userID, flag string) bool {
	enabled, err := s.deps.Store.FeatureEnabled(r.Context(), userID, flag)
	if err != nil || enabled {
		return false
	}
	s.fail(w, r, withStatus(http.StatusForbidden, contracts.ErrForbidden,
		"this feature (\""+flag+"\") is disabled for your account"))
	return true
}
