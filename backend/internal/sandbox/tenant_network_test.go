package sandbox

import (
	"context"
	"strings"
	"testing"
)

// The container's argv is the only place the isolation is real: a sandbox that
// joins the shared network can reach every other tenant's port 8080 whatever the
// policy says. These tests pin the argv, because that is what a daemon acts on.

func TestContainerArgsUseTheTenantBridgeAndNotTheSharedNetwork(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{
		Image:             "sandbox:1.11.0",
		DataRoot:          "/data/sandboxes",
		Network:           "piforge-sandboxes",
		NetworkMode:       NetworkTenant,
		APIPort:           8080,
		APIKey:            "sandbox-key",
		ContainerEndpoint: true,
	}}
	args := provider.containerArgs(context.Background(), "piforge-abc", "usr_alice", "prj_1", "/data/sandboxes/usr_alice/prj_1", "sandbox-key")
	tenant := provider.TenantNetworkName("usr_alice")
	if !strings.HasPrefix(tenant, DefaultTenantNetworkPrefix) {
		t.Fatalf("tenant network %q does not use the default prefix", tenant)
	}
	assertArgsContain(t, args, "--network", tenant)
	// A container on two bridges is reachable from both, so the shared network
	// name must not appear at all.
	for _, arg := range args {
		if arg == "piforge-sandboxes" {
			t.Fatalf("the shared sandbox network is still in the argv: %v", args)
		}
	}
	// The same tenant always resolves to the same bridge, and two tenants never
	// share one.
	if again := provider.TenantNetworkName("usr_alice"); again != tenant {
		t.Fatalf("tenant network is not stable: %q then %q", tenant, again)
	}
	if other := provider.TenantNetworkName("usr_bob"); other == tenant {
		t.Fatalf("two tenants resolved to one bridge %q", tenant)
	}
}

// The tenant id itself must not be the network name: the name is visible to every
// process that can reach the daemon.
func TestTenantNetworkNameDoesNotCarryTheUserID(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{NetworkMode: NetworkTenant}}
	name := provider.TenantNetworkName("usr_alice")
	if strings.Contains(name, "alice") {
		t.Fatalf("network %q contains the tenant id", name)
	}
	if got := len(strings.TrimPrefix(name, DefaultTenantNetworkPrefix)); got != 12 {
		t.Fatalf("hash suffix is %d hex characters, want 12: %q", got, name)
	}
	// A prefix is configuration, and an operator may need one (a shared host with
	// other products on it).
	custom := &Docker{cfg: DockerConfig{NetworkMode: NetworkTenant, TenantNetworkPrefix: "acme-t-"}}
	if got := custom.TenantNetworkName("usr_alice"); !strings.HasPrefix(got, "acme-t-") {
		t.Fatalf("custom prefix ignored: %q", got)
	}
}

// The default is deliberately the historical shared behaviour: a deployment that
// says nothing must not start creating networks. "deny by default" is the
// deployment's choice, made in the compose file and the runner's flags.
func TestNetworkModeParsing(t *testing.T) {
	cases := map[string]NetworkMode{"": NetworkShared, "shared": NetworkShared, "TENANT": NetworkTenant, " tenant ": NetworkTenant}
	for raw, want := range cases {
		got, err := ParseNetworkMode(raw)
		if err != nil {
			t.Fatalf("ParseNetworkMode(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseNetworkMode(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseNetworkMode("isolated"); err == nil || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("an unknown mode was accepted: %v", err)
	}
}

// Container endpoints (the control plane reaching a sandbox by name) used to
// require a *configured* shared network. Under per-tenant bridges there is no
// such name: the network is created at each create, and the name resolves on the
// tenant's bridge. This is what made the first tenant create fail with "container
// endpoints need a network to resolve container names on".
func TestContainerEndpointAcceptsTenantNetworks(t *testing.T) {
	if _, err := NewDockerAPI(DockerConfig{
		DataRoot:          t.TempDir(),
		APIPort:           8080,
		ContainerEndpoint: true,
		NetworkMode:       NetworkTenant,
	}); err != nil {
		t.Fatalf("tenant mode rejected a container endpoint with no shared network: %v", err)
	}
	// Shared mode still needs one: there is nothing else to resolve names on.
	if _, err := NewDockerAPI(DockerConfig{
		DataRoot:          t.TempDir(),
		APIPort:           8080,
		ContainerEndpoint: true,
	}); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("shared mode accepted a container endpoint with no network: %v", err)
	}
}

func TestContainerArgsApplyTheTenantLimits(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{
		Image:    "sandbox:1.11.0",
		DataRoot: "/data/sandboxes",
		APIPort:  8080,
		CPUs:     "1",
		Memory:   "2g",
		Pids:     256,
		LimitsFor: func(_ context.Context, userID string) Limits {
			if userID == "usr_heavy" {
				return Limits{CPUs: "8", Memory: "16g", Pids: 4096}
			}
			return Limits{CPUs: "1", Memory: "2g", Pids: 256}
		},
	}}
	heavy := provider.containerArgs(context.Background(), "piforge-a", "usr_heavy", "prj_1", "/w", "k")
	assertArgsContain(t, heavy, "--cpus", "8")
	assertArgsContain(t, heavy, "--memory", "16g")
	assertArgsContain(t, heavy, "--pids-limit", "4096")

	light := provider.containerArgs(context.Background(), "piforge-b", "usr_light", "prj_2", "/w", "k")
	assertArgsContain(t, light, "--cpus", "1")
	assertArgsContain(t, light, "--memory", "2g")
	assertArgsContain(t, light, "--pids-limit", "256")

	// A zero process limit is "unlimited", not "--pids-limit 0": Docker reads 0
	// as unlimited, but sending it would say the deployment decided something it
	// did not.
	noPids := (&Docker{cfg: DockerConfig{Image: "i", DataRoot: "/data/sandboxes"}}).
		containerArgs(context.Background(), "piforge-c", "usr_1", "prj_1", "/w", "k")
	assertArgsAbsent(t, noPids, "--pids-limit")
	assertArgsAbsent(t, noPids, "--cpus")
	assertArgsAbsent(t, noPids, "--memory")
}

// The limits a container actually runs with are read back from Docker, in
// Docker's units, because the sandbox row records facts and not intentions.
func TestParseCPUAndMemoryUnits(t *testing.T) {
	if got := ParseCPUMilli("2"); got != 2000 {
		t.Fatalf("ParseCPUMilli(2) = %d", got)
	}
	if got := ParseCPUMilli("0.5"); got != 500 {
		t.Fatalf("ParseCPUMilli(0.5) = %d", got)
	}
	if got := ParseCPUMilli(""); got != 0 {
		t.Fatalf("ParseCPUMilli(\"\") = %d, want 0 (no limit)", got)
	}
	cases := map[string]int{"2g": 2048, "2gb": 2048, "512m": 512, "512mb": 512, "1024k": 1, "1073741824": 1024}
	for raw, want := range cases {
		got, err := ParseMemMB(raw)
		if err != nil {
			t.Fatalf("ParseMemMB(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseMemMB(%q) = %d, want %d", raw, got, want)
		}
	}
	// A typo must be rejected: silently reading "2x" as zero would report "no
	// limit" for a deployment that meant to set one.
	for _, bad := range []string{"2x", "0", "-1", "g"} {
		if _, err := ParseMemMB(bad); err == nil {
			t.Fatalf("ParseMemMB(%q) was accepted", bad)
		}
	}
	if err := (Limits{CPUs: "0", Memory: "2g"}).Validate(); err == nil {
		t.Fatal("zero CPUs was accepted as a limit")
	}
	if err := (Limits{CPUs: "2", Memory: "2g", Pids: 128}).Validate(); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}
}
