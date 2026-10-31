package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/tenantegress"
)

// Per-tenant egress: the deployment's policy, resolved once at startup and handed
// to the two places that need it.
//
// The policy itself lives in internal/tenantegress (the file format, the merge
// rules and the validation). What is here is the wiring: the flags that seed the
// default, the decision about what an *unconfigured* deployment means, and the
// adapters that give the providers and the admin report the shape each wants.
//
// The enforcement is not here and is not the control plane's: the runner (or, for
// -sandbox docker, this process) creates the tenant's bridge, and the bridge's
// `--internal` flag is the whole of "none". That is why the resolved mode travels
// with each create request (sandbox.RunnerConfig.EgressFor) instead of the runner
// reading the file a second time.

// resolveTenantEgress builds the policy from the flags and the optional file.
//
// Three cases, and the third is the one worth reading twice:
//   - a file was configured: it decides, with -tenant-egress-mode as the fallback
//     default (exactly like the quota file);
//   - only -tenant-egress-mode was set: that mode for every tenant;
//   - neither, but sandboxes get per-tenant bridges: the default is *none*. The
//     boundary exists in that shape, so it starts closed; an operator who wants
//     the internet has to say so, which is the only way "open" can mean anything;
//   - neither and one shared bridge: no policy at all. A per-tenant answer is
//     impossible there, and the boot log plus the admin page both say the
//     deployment's egress is open rather than pretending otherwise.
func resolveTenantEgress(file, modeFlag string, networkMode sandbox.NetworkMode) (*tenantegress.Policy, error) {
	fallback := sandbox.EgressNone
	if trimmed := strings.TrimSpace(modeFlag); trimmed != "" {
		mode, err := sandbox.ParseEgressMode(trimmed)
		if err != nil {
			return nil, fmt.Errorf("-tenant-egress-mode: %w", err)
		}
		fallback = mode
	}
	if path := strings.TrimSpace(file); path != "" {
		policy, err := tenantegress.LoadFile(path, tenantegress.Egress{Mode: fallback})
		if err != nil {
			return nil, err
		}
		return policy, nil
	}
	if strings.TrimSpace(modeFlag) != "" {
		return tenantegress.New(tenantegress.Egress{Mode: fallback}, "flags"), nil
	}
	if networkMode == sandbox.NetworkTenant {
		return tenantegress.New(tenantegress.Egress{Mode: sandbox.EgressNone}, "network-mode"), nil
	}
	return nil, nil
}

// checkTenantEgress is the boot gate: a policy this deployment cannot apply must
// fail the start, naming the mode and the reason, instead of becoming a page that
// describes a boundary nobody enforces.
func checkTenantEgress(kind string, networkMode sandbox.NetworkMode, policy *tenantegress.Policy) error {
	if policy == nil {
		return nil
	}
	if kind == "local" {
		return policy.CheckHost()
	}
	return policy.Check(networkMode)
}

// egressFor resolves one tenant's mode for the providers. It is a method on the
// tenant resolver so it shares the account→email cache with the quota lookup: a
// policy may name an account by address, and both policies should resolve the
// same account the same way.
//
// It is only wired when a policy exists. A nil EgressFor means "this deployment
// configured no policy", which the providers read as their historical open
// default; an EgressFor that *returns* nothing is a policy that answered nothing,
// and that closes (sandbox.egressFor).
func (r *tenantResolver) egressFor(ctx context.Context, userID string) sandbox.EgressMode {
	if r == nil || r.egress == nil {
		return ""
	}
	entry := r.egress.For(userID, r.email(ctx, userID))
	return entry.Mode
}
