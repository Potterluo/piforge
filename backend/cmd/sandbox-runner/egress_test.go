package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The runner's fallback is what a create that carries no mode gets, and the two
// defaults are deliberately different: tenant bridges start closed, the shared
// single-host shape keeps the historical open one.
func TestRunnerEgressDefaultsCloseTenantBridges(t *testing.T) {
	got, err := runnerEgress("", "", sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("runnerEgress: %v", err)
	}
	if got != sandbox.EgressNone {
		t.Fatalf("tenant mode with no policy = %q, want none", got)
	}
	got, err = runnerEgress("", "", sandbox.NetworkShared)
	if err != nil {
		t.Fatalf("runnerEgress: %v", err)
	}
	if got != sandbox.EgressOpen {
		t.Fatalf("shared mode with no policy = %q, want the historical open", got)
	}
	// An explicit flag wins in either direction.
	if got, err = runnerEgress("open", "", sandbox.NetworkTenant); err != nil || got != sandbox.EgressOpen {
		t.Fatalf("explicit open = %q, %v", got, err)
	}
	if got, err = runnerEgress("none", "", sandbox.NetworkShared); err != nil || got != sandbox.EgressNone {
		t.Fatalf("explicit none = %q, %v", got, err)
	}
	// A mode this process cannot enforce fails the boot.
	if _, err := runnerEgress("allowlist", "", sandbox.NetworkTenant); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("allowlist at boot = %v", err)
	}
	if _, err := runnerEgress("openn", "", sandbox.NetworkTenant); err == nil {
		t.Fatal("a typo was accepted")
	}
}

// A file is read and checked even though the control plane sends each tenant's
// mode: a runner that would accept an unenforceable policy must refuse to start
// instead of waiting for the first request that names it.
func TestRunnerEgressReadsAndChecksTheFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "egress.json")
	body := `{"default": {"mode": "open"}, "tenants": {}}`
	if err := os.WriteFile(good, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runnerEgress("", good, sandbox.NetworkTenant)
	if err != nil {
		t.Fatalf("runnerEgress: %v", err)
	}
	if got != sandbox.EgressOpen {
		t.Fatalf("the file's default did not become the fallback: %q", got)
	}

	allowlist := filepath.Join(dir, "allowlist.json")
	if err := os.WriteFile(allowlist, []byte(`{"default": {"mode": "allowlist", "allow": [{"host": "x.org", "ports": [443]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerEgress("", allowlist, sandbox.NetworkTenant); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("an allowlist file was accepted: %v", err)
	}

	// none on a shared network is refused: the only honest answer there.
	closed := filepath.Join(dir, "closed.json")
	if err := os.WriteFile(closed, []byte(`{"default": {"mode": "none"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerEgress("", closed, sandbox.NetworkShared); err == nil || !strings.Contains(err.Error(), "shared network") {
		t.Fatalf("none on a shared network was accepted: %v", err)
	}

	if _, err := runnerEgress("", filepath.Join(dir, "missing.json"), sandbox.NetworkTenant); err == nil {
		t.Fatal("a missing policy file was accepted")
	}
}
