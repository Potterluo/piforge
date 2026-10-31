package httpapi

import (
	"context"
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

	// Two different questions, two different numbers. `agents_live` counts agent
	// processes: a kept-warm child is alive between turns, and merely opening an
	// idle fork starts one. `runs_running` counts turns actually in flight, which
	// is what 「运行中的 agent」 used to claim while reporting the process count.
	running := 0
	runsRunning := 0
	if s.hub != nil {
		running = s.hub.liveCount()
		runsRunning = s.hub.runsInFlight(r.Context())
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
		// The honest split an operator needs: a warm process is not a busy one.
		"agents_live":  running,
		"runs_running": runsRunning,
		"sandboxes":    stats.Sandboxes,
		"sandboxes_up": stats.SandboxesUp,
		"providers":    stats.Providers,
		"skills":       stats.Skills,
		"mcp_servers":  stats.MCP,
		"events":       stats.Events,
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
		// The tenant quota policy and each tenant's effective limits: the answer to
		// "what is this account allowed to use", next to what it is using.
		"tenant_limits": s.tenantLimitsReport(r.Context(), user.ID),
	})
}

// tenantLimitRow is one tenant's line on the admin status page.
type tenantLimitRow struct {
	UserID       string `json:"user_id"`
	Email        string `json:"email,omitempty"`
	CPUs         string `json:"cpus,omitempty"`
	Memory       string `json:"memory,omitempty"`
	Pids         int    `json:"pids,omitempty"`
	MaxSandboxes int    `json:"max_sandboxes"`
	// Sandboxes is how many the tenant holds right now, which is what the cap
	// applies to.
	Sandboxes int `json:"sandboxes"`
	// Effective is true when the container limits are not "unlimited", so the page
	// can distinguish a configured tenant from one the deployment left alone.
	Effective bool `json:"effective"`
	// Override is true when the policy names this tenant explicitly.
	Override bool `json:"override"`
	// Egress is this tenant's effective outbound-network mode ("none", "open", or
	// "allowlist" for a policy the deployment refuses to start with). It is on the
	// row because "who can reach the internet" is a per-tenant question, and the
	// answer belongs next to the tenant's other limits.
	Egress string `json:"egress,omitempty"`
	// EgressOverride is true when the egress policy names this tenant.
	EgressOverride bool `json:"egress_override,omitempty"`
}

// tenantLimitsReport is the admin status block.
type tenantLimitsReport struct {
	CPUs         string `json:"cpus,omitempty"`
	Memory       string `json:"memory,omitempty"`
	Pids         int    `json:"pids,omitempty"`
	MaxSandboxes int    `json:"max_sandboxes"`
	// Source names where the policy came from ("flags" or the file path): a limit
	// an operator cannot find the line for is a limit they cannot change.
	Source string `json:"source"`
	// NetworkMode is "shared" or "tenant".
	NetworkMode string `json:"network_mode"`
	// Enforced is false when the deployment configured nothing: the page has to
	// tell "unlimited" apart from "we did not think about it".
	Enforced bool             `json:"enforced"`
	Tenants  []tenantLimitRow `json:"tenants"`

	// EgressMode is the default outbound-network posture ("none", "open", or
	// "allowlist" for a file the deployment refuses to start with). EgressSource
	// names where it came from; EgressConfigured is false when this deployment
	// configured no egress policy at all, which is open and is said out loud.
	EgressMode       string `json:"egress_mode"`
	EgressSource     string `json:"egress_source"`
	EgressConfigured bool   `json:"egress_configured"`
	// EgressApplied is false when there is no container to apply it to: with
	// -sandbox local the agent shares the host's network namespace, so the mode is
	// reported but it is not a boundary (and the page says so).
	EgressApplied bool `json:"egress_applied"`
}

// tenantLimitsReport builds the tenant block from the policy and one count query.
//
// Every account is listed, not only the ones with an override: a tenant with no
// entry runs with the default, and "there is no row for me" is exactly the
// confusion this block exists to remove.
//
// The egress policy is reported in the same block, per tenant, for the same
// reason: "what is this account allowed to use" and "what can it reach" are one
// operator question, and a mode an operator cannot see is a mode nobody can act
// on.
func (s *server) tenantLimitsReport(ctx context.Context, callerID string) tenantLimitsReport {
	report := tenantLimitsReport{
		NetworkMode:      s.deps.SandboxNetworkMode,
		Source:           "none",
		Tenants:          []tenantLimitRow{},
		EgressApplied:    s.deps.SandboxEgressApplied,
		EgressConfigured: s.deps.TenantEgress != nil,
		EgressMode:       "open",
		EgressSource:     "none",
	}
	if report.NetworkMode == "" {
		report.NetworkMode = "shared"
	}
	counts := map[string]int{}
	if s.deps.Store != nil {
		if got, err := s.deps.Store.SandboxCountsByUser(ctx); err == nil {
			counts = got
		} else {
			s.log.WarnContext(ctx, "admin status: sandbox counts unavailable", "error", err.Error())
		}
	}
	if s.deps.TenantEgress != nil {
		egress := s.deps.TenantEgress
		report.EgressMode = string(egress.Default().Mode)
		report.EgressSource = egress.Source()
	}
	if s.deps.TenantLimits == nil && s.deps.TenantEgress == nil {
		return report
	}
	policy := s.deps.TenantLimits
	if policy != nil {
		def := policy.Default()
		report.CPUs, report.Memory, report.Pids = def.CPUs, def.Memory, def.Pids
		report.MaxSandboxes = def.MaxSandboxes
		report.Source = policy.Source()
		report.Enforced = !def.Sandbox().IsZero() || def.MaxSandboxes > 0
	}

	users, err := s.deps.Store.AdminListUsers(ctx, callerID)
	if err != nil {
		s.log.WarnContext(ctx, "admin status: tenant list unavailable", "error", err.Error())
		return report
	}
	named := map[string]bool{}
	var egressNamed map[string]bool
	if policy != nil {
		for _, key := range policy.Tenants() {
			named[strings.ToLower(key)] = true
		}
	}
	if s.deps.TenantEgress != nil {
		egressNamed = map[string]bool{}
		for _, key := range s.deps.TenantEgress.Tenants() {
			egressNamed[strings.ToLower(key)] = true
		}
	}
	for _, user := range users {
		row := tenantLimitRow{UserID: user.ID, Email: user.Email, Sandboxes: counts[user.ID]}
		if policy != nil {
			limits := policy.For(user.ID, user.Email)
			row.CPUs, row.Memory, row.Pids = limits.CPUs, limits.Memory, limits.Pids
			row.MaxSandboxes = limits.MaxSandboxes
			row.Effective = !limits.Sandbox().IsZero() || limits.MaxSandboxes > 0
			row.Override = named[strings.ToLower(user.ID)] ||
				(user.Email != "" && named[strings.ToLower(user.Email)])
		}
		if s.deps.TenantEgress != nil {
			row.Egress = string(s.deps.TenantEgress.For(user.ID, user.Email).Mode)
			row.EgressOverride = egressNamed[strings.ToLower(user.ID)] ||
				(user.Email != "" && egressNamed[strings.ToLower(user.Email)])
		}
		report.Tenants = append(report.Tenants, row)
	}
	return report
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
