package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantegress"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
)

// Per-tenant quotas and networks: the deployment's policy, resolved once at
// startup and handed to the two places that enforce it.
//
// The policy itself lives in internal/tenantlimits (the file format, the merge
// rules and the validation). What is here is the wiring: the flags that seed the
// default, the store lookup that lets a policy file name an account by its email,
// and the adapters that give the provider and the provisioner the shape each of
// them wants. It is one resolver value, not two, so the container limits and the
// concurrency guard can never disagree about what a tenant's quota is.

// tenantResolver answers "what is this tenant's quota" for the control plane.
type tenantResolver struct {
	policy *tenantlimits.Policy
	// egress is the per-tenant outbound-network policy, resolved by
	// newTenantEgress (tenant_egress.go). Nil means the deployment configured
	// none: the providers keep their historical open shape and both the boot log
	// and the admin page say so.
	egress *tenantegress.Policy
	store  *store.Store
	// emails caches user id -> address. A policy is consulted on every sandbox
	// create and on every admin status read; the mapping never changes for an
	// account that exists, and the cache only holds ids this process was asked
	// about.
	mu     sync.Mutex
	emails map[string]string
}

// newTenantResolver builds the policy from the flags and the optional file.
//
// Precedence is deliberate and stated in docs/SANDBOX-DEPLOY.md §7: the flags are
// the *default* every tenant gets, the file's `default` overrides them, and a
// per-tenant entry overrides that. A deployment that configures nothing gets the
// zero limits — no CPU, memory or process cap — which is the single-user
// behaviour this product had before; the compose file sets real ones.
func newTenantResolver(st *store.Store, defaultLimits tenantlimits.Limits, file string) (*tenantResolver, error) {
	policy := tenantlimits.New(defaultLimits)
	if strings.TrimSpace(file) != "" {
		loaded, err := tenantlimits.LoadFile(strings.TrimSpace(file), defaultLimits)
		if err != nil {
			return nil, err
		}
		policy = loaded
	}
	return &tenantResolver{policy: policy, store: st, emails: map[string]string{}}, nil
}

// policyFor is the resolver both adapters share.
func (r *tenantResolver) policyFor(ctx context.Context, userID string) tenantlimits.Limits {
	if r == nil {
		return tenantlimits.Limits{}
	}
	return r.policy.For(userID, r.email(ctx, userID))
}

// email resolves an account's address for policy lookup. A failure is not fatal:
// the user id is tried first, and a missing email only means an entry keyed by
// address does not match — falling back to the default, which is what an
// unlisted tenant gets anyway.
func (r *tenantResolver) email(ctx context.Context, userID string) string {
	if r.store == nil || strings.TrimSpace(userID) == "" {
		return ""
	}
	r.mu.Lock()
	cached, ok := r.emails[userID]
	r.mu.Unlock()
	if ok {
		return cached
	}
	user, err := r.store.UserByID(ctx, userID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("tenant policy: could not resolve the account's email; using the default limits",
				"user", userID, "error", err.Error())
		}
		return ""
	}
	r.mu.Lock()
	r.emails[userID] = user.Email
	r.mu.Unlock()
	return user.Email
}

// sandboxLimits adapts the policy to what the container providers consume.
func (r *tenantResolver) sandboxLimits(ctx context.Context, userID string) sandbox.Limits {
	return r.policyFor(ctx, userID).Sandbox()
}

// validateTenantWiring rejects a deployment whose quota or network configuration
// cannot mean what it says. Both are startup errors: the cost of finding out at
// the first session is a tenant that ran unlimited.
func validateTenantWiring(kind, networkMode string, joinContainers []string, maxSandboxes int) error {
	if maxSandboxes < 0 {
		return fmt.Errorf("-tenant-max-sandboxes must not be negative (0 disables the guard)")
	}
	mode, err := sandbox.ParseNetworkMode(networkMode)
	if err != nil {
		return err
	}
	if mode == sandbox.NetworkTenant && kind == "runner" && len(joinContainers) == 0 {
		// Under the runner the control plane is a container of its own: a tenant
		// bridge nothing else joins leaves the sandbox unable to reach the gateway
		// and the runner unable to provision a checkout. Say which flag is missing.
		return fmt.Errorf("-sandbox-network-mode tenant needs -sandbox-join-containers (PIFORGE_SANDBOX_JOIN_CONTAINERS) naming the control plane and the runner, e.g. piforge,piforge-sandbox-runner")
	}
	return nil
}

// splitList reads a comma-separated flag, dropping empty entries so a trailing
// comma or an unset variable is "nothing named" rather than one item called "".
func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// emptyAs is the log-friendly form of a value that may be unset, where empty
// means "no limit" rather than "missing configuration".
func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
