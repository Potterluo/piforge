package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// apiKeyPrefix marks a PiForge token so a leaked one is recognisable in a log or a
// paste, the way `ghp_` does for GitHub.
const apiKeyPrefix = "pfk"

// handleListAPIKeys returns the caller's tokens (PRD FR-13.9).
func (s *server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	keys, err := s.deps.Store.ListAPIKeys(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
}

// handleCreateAPIKey mints a token. The plaintext is in this response only.
func (s *server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())

	var in struct {
		Name string `json:"name"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "给这个 key 起个名字，方便以后认出它"))
		return
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		s.fail(w, r, err)
		return
	}
	token := apiKeyPrefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	key, err := s.deps.Store.CreateAPIKey(r.Context(), user.ID, store.NewAPIKey{
		Name:      in.Name,
		Prefix:    token[:12],
		TokenHash: auth.HashToken(token),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Shown once, on purpose: anything that can read it back later is a token
	// store, and this deployment deliberately is not one.
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": key, "token": token})
}

// handleRevokeAPIKey makes a token unusable and keeps the row for the audit trail.
func (s *server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	key, err := s.deps.Store.RevokeAPIKey(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": key})
}

// bearerUser resolves an `Authorization: Bearer pfk_...` header.
//
// It is the second half of FR-13.9: a script must be able to drive the same API the
// UI drives. Bearer requests skip CSRF, because CSRF defends a cookie a browser
// sends automatically, and a header a caller chooses is not that.
func (s *server) bearerUser(r *http.Request) (*contracts.User, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, false
	}
	const scheme = "bearer "
	if !strings.HasPrefix(strings.ToLower(header), scheme) {
		return nil, false
	}
	token := strings.TrimSpace(header[len(scheme):])
	if !strings.HasPrefix(token, apiKeyPrefix+"_") {
		return nil, false
	}
	ctx, cancel := detach(r.Context(), 5*time.Second)
	defer cancel()
	user, err := s.deps.Store.APIKeyUser(ctx, auth.HashToken(token))
	if err != nil || user.Status != "active" {
		return nil, true
	}
	return user, true
}
