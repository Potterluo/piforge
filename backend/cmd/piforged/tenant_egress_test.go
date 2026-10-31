package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// Where the policy comes from, in the order the boot log quotes: file, flag,
// network-mode default, nothing.
func TestResolveTenantEgressSources(t *testing.T) {
	// Nothing configured, tenant bridges: closed, and it says where that came from.
	policy, err := resolveTenantEgress("", "", sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if policy == nil || policy.Default().Mode != sandbox.EgressNone {
		t.Fatalf("tenant bridges with no policy = %+v, want none", policy)
	}
	if policy.Source() != "network-mode" {
		t.Fatalf("source = %q, want network-mode", policy.Source())
	}

	// Nothing configured, one shared bridge: no policy at all, which the caller
	// reports as open. Pretending "none" there would be the silent lie.
	policy, err = resolveTenantEgress("", "", sandbox.NetworkShared)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if policy != nil {
		t.Fatalf("shared mode with no policy = %+v, want nil (open, reported)", policy)
	}

	// The flag alone.
	policy, err = resolveTenantEgress("", "open", sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if policy == nil || policy.Default().Mode != sandbox.EgressOpen || len(policy.Tenants()) != 0 {
		t.Fatalf("flag-only policy = %+v", policy)
	}

	// The file wins over the flag's default, like the quota file does.
	path := filepath.Join(t.TempDir(), "tenant-egress.json")
	if err := os.WriteFile(path, []byte(`{"default": {"mode": "none"}, "tenants": {"dev@piforge.local": {"mode": "open"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err = resolveTenantEgress(path, "open", sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if policy.Default().Mode != sandbox.EgressNone {
		t.Fatalf("the file's default did not win: %+v", policy.Default())
	}
	if got := policy.For("usr_dev", "dev@piforge.local").Mode; got != sandbox.EgressOpen {
		t.Fatalf("dev = %q, want open", got)
	}
	if err := policy.Check(sandbox.NetworkTenant); err != nil {
		t.Fatalf("Check: %v", err)
	}

	// An unparsable flag is a boot error, not a silent default.
	if _, err := resolveTenantEgress("", "isolated", sandbox.NetworkTenant); err == nil {
		t.Fatal("an unknown -tenant-egress-mode was accepted")
	}
}

// The boot gate has to refuse every shape the deployment cannot enforce, and it
// has to name the tenant when a mode is the reason.
func TestCheckTenantEgressRefusesWhatCannotBeEnforced(t *testing.T) {
	// The allowlist case has to come through a file: that is where an operator
	// would name hostnames.
	path := filepath.Join(t.TempDir(), "tenant-egress.json")
	if err := os.WriteFile(path, []byte(`{"default": {"mode": "none"}, "tenants": {"bob@piforge.local": {"mode": "allowlist", "allow": [{"host": "registry.npmjs.org", "ports": [443]}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := resolveTenantEgress(path, "", sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	err = checkTenantEgress("runner", sandbox.NetworkTenant, policy)
	if err == nil {
		t.Fatal("an allowlist policy passed the boot check")
	}
	for _, want := range []string{"bob@piforge.local", "not implemented"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("boot refusal %q does not mention %q", err, want)
		}
	}

	// -sandbox local has no bridge to close, so a closed policy is refused there
	// rather than reported as applied.
	closed, err := resolveTenantEgress("", "none", sandbox.NetworkShared)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if err := checkTenantEgress("local", sandbox.NetworkShared, closed); err == nil || !strings.Contains(err.Error(), "-sandbox local") {
		t.Fatalf("closed policy on the host placement = %v", err)
	}
	// ... while "open" is the one statement that is true on the host.
	open, err := resolveTenantEgress("", "open", sandbox.NetworkShared)
	if err != nil {
		t.Fatalf("resolveTenantEgress: %v", err)
	}
	if err := checkTenantEgress("local", sandbox.NetworkShared, open); err != nil {
		t.Fatalf("open on the host placement: %v", err)
	}
	// And nothing configured is nothing to check.
	if err := checkTenantEgress("local", sandbox.NetworkShared, nil); err != nil {
		t.Fatalf("nil policy: %v", err)
	}
}
