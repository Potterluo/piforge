package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// usersResponse is the admin user list envelope.
type usersResponse struct {
	Users []*contracts.User `json:"users"`
}

// handleAdminUsers lists every account. It is the only cross-user read in the
// API, it exposes account metadata only, and it is guarded twice: RequireUser
// in the router and an explicit role check here, so a mistake in route wiring
// still cannot leak it.
func (s *server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	users, err := s.deps.Store.AdminListUsers(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if users == nil {
		users = []*contracts.User{}
	}
	writeJSON(w, http.StatusOK, usersResponse{Users: users})
}
