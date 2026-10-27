package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// meResponse is the GET /v1/auth/me and POST /v1/auth/login envelope. csrf is
// duplicated from the readable cookie so a client that stores tokens in memory
// does not have to read document.cookie.
type meResponse struct {
	User *contracts.User `json:"user"`
	CSRF string          `json:"csrf"`
}

// handleLogin is the only endpoint that does not require a session. It is also
// the only mutation exempt from CSRF: there is no authenticated state to
// protect and no csrf cookie to echo yet.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	sess, err := s.deps.Auth.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// SetCookies also sets the X-PF-CSRF response header, which is how a
	// cross-origin dev client (different port) reads the token.
	s.deps.Auth.SetCookies(w, sess)
	writeJSON(w, http.StatusOK, meResponse{User: sess.User, CSRF: sess.CSRFToken})
}

// handleLogout revokes the current session and clears both cookies. It is
// idempotent: logging out twice is fine.
//
// It answers 200 {"ok": true} rather than 204 because web/lib/api/client.ts
// types the call as {ok: boolean} and a 204 would hand it undefined.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Auth.Logout(r.Context(), r); err != nil {
		s.fail(w, r, err)
		return
	}
	s.deps.Auth.ClearCookies(w)
	writeOK(w)
}

// handleMe returns the caller and re-issues the CSRF cookie when it is missing
// (a browser that cleared cookies but kept the session would otherwise be
// unable to mutate anything).
func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	csrf, err := s.deps.Auth.EnsureCSRF(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meResponse{User: user, CSRF: csrf})
}
