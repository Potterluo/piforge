package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// ensureTenantNetwork creates the tenant's bridge if it is missing and joins the
// containers that have to keep reaching the sandbox.
//
// The joins are the other half of the isolation: the control plane serves the
// browser, file and shell panels *through* the sandbox's API, and the runner
// provisions the checkout through the same API. Both are containers of their
// own, so both have to be on the tenant's bridge. That is a deliberate,
// documented membership: the control plane is the one process that may talk to
// every sandbox, and the sandbox has to reach the control plane's gateway for
// its model tokens.
//
// It is idempotent: `network inspect` answers whether the bridge exists, and a
// `network connect` that reports "already exists" is the normal outcome of the
// second sandbox in a tenant.
func (d *Docker) ensureTenantNetwork(ctx context.Context, userID string) error {
	if d.cfg.NetworkMode != NetworkTenant {
		return nil
	}
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
		// removes exactly those.
		if _, err := d.docker(ctx, "network", "create",
			"--label", "piforge.tenant="+name,
			"--label", "piforge.role=sandbox",
			name); err != nil {
			// A create that races another create is not a failure: the network
			// exists either way, which is all the caller needs.
			if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return err
			}
		}
		slog.Info("sandbox: created a tenant network", "network", name)
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
