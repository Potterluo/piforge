package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Per-tenant sandbox networks.
//
// The problem this solves: a single `piforge-sandboxes` bridge puts every
// tenant's sandbox on one L2 segment. Docker cannot deny traffic between
// containers on one bridge, so the only honest way to say "a sandbox cannot
// reach another sandbox" is to give the tenants different bridges. The chain in
// the image (nginx) requires a per-sandbox key, which is why cross-sandbox reads
// answered 401 in the acceptance pass — but 401 is authentication, not
// isolation, and a bug in that chain would be a cross-tenant read.
//
// Why per tenant and not per sandbox: the product's tenancy rule is "one sandbox
// per (user, project)", so a tenant owns N projects and therefore N sandboxes.
// The authorization boundary this product enforces is the *user* (a cross-user
// read is 404 on every route), so the network boundary is drawn where the
// authorization boundary already is. A network per sandbox would additionally
// separate one tenant's own projects from each other — and would need the
// control plane and the runner joined to every one of them (two extra veths per
// sandbox, a `docker network create` per sandbox, and stock Docker's default
// address pool exhausted after a handful of networks; see
// docs/SANDBOX-DEPLOY.md §7.3 for the numbers). Per tenant costs one network per
// account, which is bounded by the account list and not by sandbox churn.
//
// What this does and does not claim is written down in
// docs/EXECUTION-BOUNDARY.md §11: tenants cannot reach each other; two sandboxes
// of the *same* tenant share a bridge and can reach each other's port 8080,
// where the image's own key check still answers 401.

// NetworkMode selects how a sandbox's network membership is decided.
type NetworkMode string

const (
	// NetworkShared puts every sandbox on the one network named by
	// DockerConfig.Network. It is the pre-multi-tenant shape: every sandbox can
	// reach every other sandbox's port, and the only thing between them is the
	// per-sandbox API key.
	NetworkShared NetworkMode = "shared"
	// NetworkTenant gives each tenant its own bridge and puts a sandbox on that
	// bridge only. It is the default this deployment documents, because "deny by
	// default" is the property, and a shared bridge is not it.
	NetworkTenant NetworkMode = "tenant"
)

// DefaultTenantNetworkPrefix is the name prefix of a tenant's bridge. The suffix
// is a hash of the tenant id, not the id itself: Docker network names are
// visible to every process that can reach the daemon, and a user id is an
// account identifier that has no business being in a global namespace.
const DefaultTenantNetworkPrefix = "piforge-t-"

// ParseNetworkMode normalises a configured mode. Empty keeps the historical
// shared behaviour so an existing deployment does not change under its feet; the
// compose file and the runner set "tenant" explicitly.
func ParseNetworkMode(raw string) (NetworkMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(NetworkShared):
		return NetworkShared, nil
	case string(NetworkTenant):
		return NetworkTenant, nil
	default:
		return "", fmt.Errorf("sandbox network mode must be %q or %q, got %q",
			NetworkShared, NetworkTenant, raw)
	}
}

// TenantNetworkName is the bridge one tenant's sandboxes join.
func (d *Docker) TenantNetworkName(userID string) string {
	prefix := strings.TrimSpace(d.cfg.TenantNetworkPrefix)
	if prefix == "" {
		prefix = DefaultTenantNetworkPrefix
	}
	sum := sha256.Sum256([]byte(userID))
	return prefix + hex.EncodeToString(sum[:6])
}

// networkFor is the name a created container joins. Under NetworkTenant it is
// the tenant's bridge and the configured shared network is not used at all: a
// container on two networks is reachable from both, which would silently undo
// the isolation the mode exists for.
func (d *Docker) networkFor(userID string) string {
	if d.cfg.NetworkMode == NetworkTenant {
		return d.TenantNetworkName(userID)
	}
	return d.cfg.Network
}

// ErrEgressUnenforceable marks a create refused because the tenant's egress mode
// cannot be applied by this deployment (an unimplemented mode, a shared bridge, a
// bridge whose flag disagrees with the policy). It is a *request* error, not a
// daemon failure, so a caller can answer 400 instead of 500 — the difference
// between "you asked for something impossible" and "the host is broken".
var ErrEgressUnenforceable = errors.New("egress mode cannot be enforced")

// EgressMode is a sandbox's outbound-network posture: what it may reach beyond
// the containers on its own tenant bridge.
//
// It exists because the tenancy batch left the last "a tenant can affect the
// outside world" hole open: a sandbox on a normal bridge can `npm install`,
// `curl`, or exfiltrate, and nothing in the deployment said whether that was
// intended. The mode is a per-tenant policy (internal/tenantegress), and this
// package is where it becomes a Docker fact.
type EgressMode string

const (
	// EgressNone is "no egress beyond the control plane and the gateway": the
	// tenant's bridge is created with `docker network create --internal`, so it
	// has no default route and no NAT. The control plane and the runner are
	// joined to the bridge (see ensureTenantNetwork), so the sandbox still
	// reaches /gw/llm and /gw/mcp; the public internet is not routable, and
	// Docker's embedded DNS does not resolve external names on an internal
	// network. This is the default of the shipped isolated deployment.
	EgressNone EgressMode = "none"
	// EgressAllowlist would be "the tenant may reach these hosts/CIDRs/ports".
	// It is modelled by the policy and refused by Enforceable(): see the comment
	// there for why it is not accepted rather than silently ignored.
	EgressAllowlist EgressMode = "allowlist"
	// EgressOpen is the historical behaviour: the bridge is ordinary, Docker
	// NATs it, and the sandbox can reach whatever the host can reach. It is now
	// an explicit choice, never a default that nobody made.
	EgressOpen EgressMode = "open"
)

// ParseEgressMode normalises a configured mode. Empty is *none*, not open: a
// policy that forgot to say is a policy that wants the boundary, and the one
// value that must never be assumed is the permissive one.
func ParseEgressMode(raw string) (EgressMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(EgressNone):
		return EgressNone, nil
	case string(EgressAllowlist):
		return EgressAllowlist, nil
	case string(EgressOpen):
		return EgressOpen, nil
	default:
		return "", fmt.Errorf("sandbox egress mode must be %q, %q or %q, got %q",
			EgressNone, EgressAllowlist, EgressOpen, raw)
	}
}

// Enforceable reports whether this deployment can actually apply the mode, with
// the reason when it cannot. A mode that cannot be enforced must fail the boot
// or the create: an operator who believes egress is closed while it is open is
// worse off than one who was told the truth at startup.
func (m EgressMode) Enforceable() error {
	switch m {
	case EgressNone, EgressOpen:
		return nil
	case EgressAllowlist:
		return fmt.Errorf(`%w: egress mode "allowlist" is not implemented and is refused rather than `+
			`accepted: Docker filters a bridge, not a hostname, so a per-host allowlist needs either `+
			`a filtering proxy on the tenant's internal bridge (the only egress path) or iptables `+
			`rules installed on the host by a privileged process, and this deployment does neither. `+
			`Use "none" (closed: the gateway stays reachable) or "open" (explicit, the historical `+
			`behaviour)`, ErrEgressUnenforceable)
	default:
		return fmt.Errorf("%w: unknown egress mode %q (want none, allowlist or open)", ErrEgressUnenforceable, string(m))
	}
}

// Closed reports whether the bridge must be internal for this mode.
func (m EgressMode) Closed() bool { return m != EgressOpen }

// ensureNetwork is the one entry point that makes the egress policy real: the
// tenant bridge exists (or is created) with the bridge-level switch the mode
// needs, or the create is refused.
func (d *Docker) ensureNetwork(ctx context.Context, userID string) error {
	mode := d.egressFor(ctx, userID)
	if err := mode.Enforceable(); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if d.cfg.NetworkMode != NetworkTenant {
		if mode == EgressOpen {
			return nil
		}
		// A shared bridge is the single-host shape: there is no per-tenant
		// anything on it, and Docker cannot make one part of a bridge closed.
		// Saying so is the only honest answer; the deployment has to choose the
		// tenant network mode (or say "open" out loud).
		return fmt.Errorf("%w: egress mode %q cannot be enforced on the shared network %q: "+
			"a per-tenant egress policy needs a bridge per tenant (-sandbox-network-mode tenant); "+
			"refusing to create a sandbox whose egress policy this deployment would ignore",
			ErrEgressUnenforceable, string(mode), d.cfg.Network)
	}
	return d.ensureTenantNetwork(ctx, userID, mode)
}

// egressFor resolves one tenant's mode.
//
// Three cases, deliberately different:
//   - a policy function is configured: its answer is the truth, and an empty
//     answer is treated as "none" — a policy that returns nothing must not open
//     the network;
//   - a deployment-wide mode is configured: that value;
//   - neither: open, because this is the historical single-host shape and the
//     caller that never configured egress is not making a claim either way. The
//     boot log and the admin page both say so (cmd/piforged warns once).
func (d *Docker) egressFor(ctx context.Context, userID string) EgressMode {
	if d.cfg.EgressFor != nil {
		if mode, err := ParseEgressMode(string(d.cfg.EgressFor(ctx, userID))); err == nil {
			return mode
		} else {
			// An unparsable answer is closed, not open: the comparison is with a
			// typo, and the safe reading of a typo is "no egress".
			return EgressNone
		}
	}
	if d.cfg.Egress != "" {
		if mode, err := ParseEgressMode(string(d.cfg.Egress)); err == nil {
			return mode
		}
		return EgressNone
	}
	return EgressOpen
}

// tenantNetworkCreateArgs is the argv of `docker network create` for one tenant.
// It is a function of its own because the `--internal` flag is the whole
// enforcement: it is pinned by a unit test the way the container's `--network`
// is, and `docker network create` is the one place a wrong argv would look fine
// until an agent fetched something.
func tenantNetworkCreateArgs(name string, mode EgressMode) []string {
	args := []string{
		"network", "create",
		"--label", "piforge.tenant=" + name,
		"--label", "piforge.role=sandbox",
		// The mode is on the network, not only in a config file: `docker network
		// ls --filter label=piforge.egress=none` answers "who is closed" from the
		// daemon, which is the side that actually enforces it.
		"--label", "piforge.egress=" + string(mode),
	}
	if mode.Closed() {
		args = append(args, "--internal")
	}
	return append(args, name)
}

// ensureTenantNetwork creates the tenant's bridge if it is missing and joins the
// containers that have to keep reaching the sandbox.
//
// The joins are the other half of the isolation: the control plane serves the
// browser, file and shell panels *through* the sandbox's API, and the runner
// provisions the checkout through the same API. Both are containers of their
// own, so both have to be on the tenant's bridge. That is a deliberate,
// documented membership: the control plane is the one process that may talk to
// every sandbox, and the sandbox has to reach the control plane's gateway for
// its model tokens. It is also why `--internal` does not cut the sandbox off
// from the product: the gateway is a container on the same internal bridge.
//
// It is idempotent: `network inspect` answers whether the bridge exists, and a
// `network connect` that reports "already exists" is the normal outcome of the
// second sandbox in a tenant.
//
// An existing bridge whose `Internal` flag disagrees with the policy is a
// refusal, not a silent re-creation: the running sandboxes on it would lose
// connectivity mid-flight, and "the sandbox I am looking at" would be the one
// the policy no longer describes. The error names the command that fixes it.
func (d *Docker) ensureTenantNetwork(ctx context.Context, userID string, mode EgressMode) error {
	name := d.TenantNetworkName(userID)
	// `network ls --filter name=^…$` rather than `network inspect`: the listing is
	// an exact-name question with an empty answer for "absent", while inspect
	// reports absence as a failure whose text differs between Docker releases and
	// would have to be parsed to tell "missing" from "the daemon is down".
	listed, err := d.docker(ctx, "network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if !strings.Contains(listed, name) {
		// Labels make the bridges findable without a row: an operator can list
		// `label=piforge.tenant` and see every tenant's network, and `-Down`
		// removes exactly those. `--internal` (when the tenant's mode is closed)
		// is the enforcement itself: no default route, no NAT.
		if _, err := d.docker(ctx, tenantNetworkCreateArgs(name, mode)...); err != nil {
			// A create that races another create is not a failure: the network
			// exists either way, which is all the caller needs.
			if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return err
			}
		}
		slog.Info("sandbox: created a tenant network",
			"network", name, "egress", string(mode), "internal", mode.Closed())
	} else if err := d.checkTenantNetworkEgress(ctx, name, mode); err != nil {
		return err
	}
	for _, container := range d.cfg.JoinContainers {
		container = strings.TrimSpace(container)
		if container == "" {
			continue
		}
		if _, err := d.docker(ctx, "network", "connect", name, container); err != nil {
			lower := strings.ToLower(err.Error())
			// "already exists" is the second sandbox of a tenant; a container
			// that is not running (a stopped control plane during a restart) is
			// reported and skipped rather than failing the create — the network
			// membership is repaired on the next Ensure.
			if strings.Contains(lower, "already exists") || strings.Contains(lower, "is already") {
				continue
			}
			return err
		}
		slog.Info("sandbox: joined a container to a tenant network",
			"network", name, "container", container)
	}
	return nil
}

// dockerBool reads a `docker network inspect --format "{{.Internal}}"` answer.
//
// It is deliberately its own function and deliberately strict: only the exact
// answer "true" means internal, so an empty string from a Docker that changed its
// format closes nothing by accident — the caller reports a mismatch instead.
func dockerBool(raw string) bool { return strings.EqualFold(strings.TrimSpace(raw), "true") }

// checkTenantNetworkEgress compares an existing bridge's `Internal` flag with
// the tenant's policy and refuses a mismatch.
//
// It refuses instead of recreating for two reasons. Recreating the bridge under
// running containers cuts them off mid-turn, and — worse — the sandbox that is
// already up would keep its open route while the admin page reported the new,
// closed policy. A create that fails with a named command is a state an operator
// can see and fix; a bridge that silently disagrees with the policy is the exact
// "believes it is closed while it is open" failure this feature exists to end.
func (d *Docker) checkTenantNetworkEgress(ctx context.Context, name string, mode EgressMode) error {
	got, err := d.docker(ctx, "network", "inspect", "--format", "{{.Internal}}", name)
	if err != nil {
		return err
	}
	internal := dockerBool(got)
	if internal == mode.Closed() {
		return nil
	}
	current := EgressOpen
	if internal {
		current = EgressNone
	}
	return fmt.Errorf("%w: tenant network %s was created %q but this tenant's egress policy is %q: "+
		"a network's internal flag cannot be changed in place, and recreating it would cut off the "+
		"sandboxes already on it. Remove it with `docker network rm %s` (its sandboxes have to be "+
		"recreated) and the next create will build it %q",
		ErrEgressUnenforceable, name, string(current), string(mode), name, string(mode))
}
