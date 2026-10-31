package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/runtimedirs"
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
				"该邮箱已经注册过了"))
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

// handleAdminStatus is the operator's one screen (PRD FR-16.5): how many tenants,
// how many sessions are live, how many sandboxes are up, and how many agents are
// actually running. Counts only — this is a self-hosted tool, not a metrics
// pipeline, and the page is opened when something already feels wrong.
func (s *server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	stats, err := s.deps.Store.Stats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	running := 0
	if s.hub != nil {
		running = s.hub.liveCount()
	}

	// Sandbox browsers are processes, not sessions: a leaked one is invisible in
	// every count above. Reporting both the number and the sandboxes is what lets
	// an operator tell "the panel I am looking at" from "a browser nobody closed"
	// (QA-16). A deployment without the browser feature reports zero.
	browsersRunning := 0
	browserSandboxes := []string{}
	browserIdleTimeout := ""
	if s.deps.Browser != nil {
		browsersRunning = s.deps.Browser.Len()
		browserSandboxes = s.deps.Browser.SandboxIDs()
		browserIdleTimeout = s.deps.Browser.IdleTimeout().String()
	}

	// Per-directory disk usage for the runtime inventory. This is the answer to
	// "is something growing without an owner?" that does not require shelling
	// into the container; the janitor's own log says what it reclaimed.
	storage := s.storageUsage()
	var storageBytes int64
	for _, u := range storage {
		storageBytes += u.Bytes
	}
	// FR-5.8 made visible: every sandbox's measured checkout next to the budget it
	// is measured against. "Who is close" is the question the runtime-directory
	// block cannot answer by itself — it reports the sandboxes root as one number.
	sandboxUsage := s.sandboxUsage(r.Context())

	writeJSON(w, http.StatusOK, map[string]any{
		"users":          stats.Users,
		"users_active":   stats.UsersActive,
		"projects":       stats.Projects,
		"sessions":       stats.Sessions,
		"sessions_live":  stats.SessionsLive,
		"agents_running": running,
		"sandboxes":      stats.Sandboxes,
		"sandboxes_up":   stats.SandboxesUp,
		"providers":      stats.Providers,
		"skills":         stats.Skills,
		"mcp_servers":    stats.MCP,
		"events":         stats.Events,
		// The lazy-mount fix makes this count meaningful: it stays at zero until a
		// human selects the browser panel, and drops back after the idle timeout.
		"browsers_running":   browsersRunning,
		"browser_sandboxes":  browserSandboxes,
		"browser_idle_after": browserIdleTimeout,
		// FR-3.3: the effective cap, not the compile-time default. "What is it set
		// to" has to have an answer that does not involve reading the flag.
		"parallel_limit":     s.parallelLimit(),
		"parallel_limit_max": MaxParallelLimit,
		// The runtime directory lifecycle: one row per directory the server
		// creates under -data, with direct-entry count and recursive bytes.
		"storage":             storage,
		"storage_total_bytes": storageBytes,
		// Per-sandbox usage against -sandbox-disk-quota (0 = enforcement off, the
		// measurement is still reported so an operator can size a budget).
		"sandbox_usage":      sandboxUsage,
		"sandbox_disk_quota": s.diskQuota.Limit(),
	})
}

// storageUsage returns the cached per-directory scan. The admin page polls every
// 15 seconds; walking every checkout that often would make an idle screen a disk
// scan, so the result is reused for a minute. A test can pin Now to control it.
func (s *server) storageUsage() []runtimedirs.Usage {
	if strings.TrimSpace(s.deps.DataDir) == "" {
		return []runtimedirs.Usage{}
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.storage != nil && s.now().Sub(s.storageAt) < time.Minute {
		out := make([]runtimedirs.Usage, len(s.storage))
		copy(out, s.storage)
		return out
	}
	s.storage = runtimedirs.MeasureAll(s.deps.DataDir, runtimedirs.DefaultMaxFiles)
	s.storageAt = s.now()
	out := make([]runtimedirs.Usage, len(s.storage))
	copy(out, s.storage)
	return out
}
