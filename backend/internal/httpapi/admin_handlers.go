package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
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

// handleAdminCreateUser creates an account.
//
// Admin can create accounts because a self-hosted deployment has no other way to
// onboard the second tenant: there is no public signup by design (PRD §3).
func (s *server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	caller := auth.MustUser(r.Context())
	if caller.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}

	var in struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Email) == "" || !strings.Contains(in.Email, "@") {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "a valid email is required"))
		return
	}
	if len(in.Password) < 8 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "the password must be at least 8 characters"))
		return
	}
	role := contracts.Role(in.Role)
	if role == "" {
		role = contracts.RoleUser
	}
	if role != contracts.RoleUser && role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "role must be user or admin"))
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	user, err := s.deps.Store.CreateUser(r.Context(), store.NewUser{
		Email:        strings.TrimSpace(strings.ToLower(in.Email)),
		DisplayName:  strings.TrimSpace(in.DisplayName),
		PasswordHash: hash,
		Role:         role,
	})
	if err != nil {
		// The one conflict this endpoint can produce is a taken address, and
		// "create user ...: conflict" is not a sentence anybody wants to read.
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, apiErr(contracts.ErrConflict,
				"that email already has an account"))
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, usersResponse{Users: []*contracts.User{user}})
}

// handleAdminUpdateUser changes a role, a status or a password.
func (s *server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	caller := auth.MustUser(r.Context())
	if caller.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	target := pathID(r)

	var in struct {
		Role     *string `json:"role"`
		Status   *string `json:"status"`
		Password *string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	if in.Role != nil {
		if err := s.deps.Store.SetUserRole(r.Context(), caller.ID, target, contracts.Role(*in.Role)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if in.Status != nil {
		// Disabling yourself would log this request out mid-flight and leave the
		// deployment with an admin nobody can use.
		if target == caller.ID && *in.Status == "disabled" {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "you cannot disable your own account"))
			return
		}
		if err := s.deps.Store.SetUserStatus(r.Context(), target, *in.Status); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if in.Password != nil {
		if len(*in.Password) < 8 {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "the password must be at least 8 characters"))
			return
		}
		hash, err := auth.HashPassword(*in.Password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if err := s.deps.Store.SetPasswordHash(r.Context(), target, hash); err != nil {
			s.fail(w, r, err)
			return
		}
		// A new password must invalidate the sessions opened with the old one.
		if err := s.deps.Store.DeleteUserAuthSessions(r.Context(), target); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	user, err := s.deps.Store.UserByID(r.Context(), target)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, usersResponse{Users: []*contracts.User{user}})
}
