package sandboxrunner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The runner is the process that creates containers, so it is the last place
// where the shared network can be re-introduced by accident: a create request
// that asks for a tenant bridge must not also carry the shared network name.
func TestNetworkPlanPrefersTheTenantBridge(t *testing.T) {
	shared := &DockerProvider{opts: DockerOptions{Network: "piforge-sandboxes"}}

	mode, network := shared.networkPlan(sandbox.RunnerRequest{Network: "piforge-sandboxes"})
	if mode != sandbox.NetworkShared || network != "piforge-sandboxes" {
		t.Fatalf("default plan = %q/%q", mode, network)
	}

	// The control plane asks for tenant networks per create.
	mode, network = shared.networkPlan(sandbox.RunnerRequest{TenantNetwork: true, Network: "piforge-sandboxes"})
	if mode != sandbox.NetworkTenant || network != "" {
		t.Fatalf("tenant plan kept a shared network: %q/%q", mode, network)
	}

	// The runner requires them for every create, whatever the request says.
	tenantOnly := &DockerProvider{opts: DockerOptions{Network: "piforge-sandboxes", NetworkMode: sandbox.NetworkTenant}}
	mode, network = tenantOnly.networkPlan(sandbox.RunnerRequest{Network: "piforge-sandboxes"})
	if mode != sandbox.NetworkTenant || network != "" {
		t.Fatalf("runner-forced tenant plan = %q/%q", mode, network)
	}
}

// A request's limits win over the runner's defaults, and the runner's defaults
// win over nothing: that is the order the control plane relies on when it sends a
// tenant's quota with the create.
func TestFirstIntKeepsTheRequestValue(t *testing.T) {
	if got := firstInt(0, 512); got != 512 {
		t.Fatalf("firstInt(0, 512) = %d", got)
	}
	if got := firstInt(4096, 512); got != 4096 {
		t.Fatalf("firstInt(4096, 512) = %d", got)
	}
	if got := firstInt(0, 0); got != 0 {
		t.Fatalf("firstInt(0, 0) = %d", got)
	}
}

// The provider that only talks to an existing container can still be built in a
// tenant-network deployment: it never creates anything, so it must not require a
// network to invent.
func TestAPIProviderBuildsUnderTenantNetworks(t *testing.T) {
	provider := &DockerProvider{opts: DockerOptions{
		Network:             "piforge-sandboxes",
		NetworkMode:         sandbox.NetworkTenant,
		JoinContainers:      []string{"piforge"},
		TenantNetworkPrefix: "piforge-t-",
		DataRoot:            t.TempDir(),
	}}
	if _, err := provider.api("key"); err != nil {
		t.Fatalf("api provider: %v", err)
	}
}

// A create with a tenant limit is sent as-is over the wire, so the runner cannot
// silently apply its own default instead.
func TestRunnerRequestCarriesTenantLimits(t *testing.T) {
	request := sandbox.RunnerRequest{UserID: "usr_1", ProjectID: "prj_1", CPUs: "3", Memory: "6g", Pids: 700, TenantNetwork: true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"cpus":"3"`, `"memory":"6g"`, `"pids":700`, `"tenant_network":true`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("request JSON %s does not contain %s", body, want)
		}
	}
}
