package tenantegress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// A tenant with no entry gets the default; an entry is looked up by user id or
// by the address an operator writes.
func TestForResolvesTheOverrideOrTheDefault(t *testing.T) {
	policy, err := Parse(`{
		"default": {"mode": "none"},
		"tenants": {"dev@piforge.local": {"mode": "open"}}
	}`, Egress{Mode: sandbox.EgressNone})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := policy.For("usr_dev", "DEV@piforge.local"); got.Mode != sandbox.EgressOpen {
		t.Fatalf("dev resolved to %q, want open (the address is case-insensitive)", got.Mode)
	}
	if got := policy.For("usr_other", "other@piforge.local"); got.Mode != sandbox.EgressNone {
		t.Fatalf("an unlisted tenant resolved to %q, want the default none", got.Mode)
	}
	// The user id works too: it is what the caller always has.
	byID, err := Parse(`{"default": {"mode": "none"}, "tenants": {"usr_carol": {"mode": "open"}}}`, Egress{Mode: sandbox.EgressNone})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := byID.For("usr_carol", ""); got.Mode != sandbox.EgressOpen {
		t.Fatalf("carol by id resolved to %q", got.Mode)
	}
	if got := byID.Tenants(); len(got) != 1 || got[0] != "usr_carol" {
		t.Fatalf("Tenants() = %v", got)
	}
	if byID.Source() != "inline" {
		t.Fatalf("Source() = %q", byID.Source())
	}
}

// An empty mode in a file is "none", not "open": the permissive value is never
// what a policy means when it says nothing.
func TestAnEntryThatNamesNoModeInheritsTheDefault(t *testing.T) {
	policy, err := Parse(`{"default": {"mode": "open"}, "tenants": {"usr_a": {}}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := policy.For("usr_a", "").Mode; got != sandbox.EgressOpen {
		t.Fatalf("usr_a = %q, want the default open", got)
	}
	// The zero value of Egress.Mode parses as none, which is what the sandbox
	// provider enforces for an unanswered policy.
	none, err := Parse(`{"default": {"mode": ""}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := none.Default().Mode; got != sandbox.EgressNone {
		t.Fatalf("explicit empty mode = %q, want none", got)
	}
}

// An allowlist is validated even though it is refused for enforcement: the
// operator gets a message about the entry they got wrong, not only about the
// mode, and a file that names a host the product cannot filter is an error the
// first time it is read.
func TestAllowlistEntriesAreValidated(t *testing.T) {
	ok := `{"default": {"mode": "none"}, "tenants": {"usr_a": {"mode": "allowlist", "allow": [
		{"host": "registry.npmjs.org", "ports": [443]},
		{"cidr": "203.0.113.0/24", "ports": [443, 80]}
	]}}}`
	policy, err := Parse(ok, Egress{})
	if err != nil {
		t.Fatalf("a valid allowlist was rejected: %v", err)
	}
	entry := policy.For("usr_a", "")
	if entry.Mode != sandbox.EgressAllowlist || len(entry.Allow) != 2 {
		t.Fatalf("usr_a = %+v", entry)
	}
	if got := entry.Describes(); got != "allowlist(2 entries)" {
		t.Fatalf("Describes() = %q", got)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"wildcard host", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"host": "*.npmjs.org", "ports": [443]}]}}}`, "wildcard"},
		{"scheme in host", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"host": "https://npmjs.org", "ports": [443]}]}}}`, "bare name"},
		{"both host and cidr", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"host": "x.org", "cidr": "10.0.0.0/8", "ports": [443]}]}}}`, "one destination"},
		{"neither", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"ports": [443]}]}}}`, "host"},
		{"bad cidr", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"cidr": "10.0.0.0/99", "ports": [443]}]}}}`, "cidr"},
		{"no ports", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"host": "x.org"}]}}}`, "ports"},
		{"port range", `{"tenants": {"a": {"mode": "allowlist", "allow": [{"host": "x.org", "ports": [70000]}]}}}`, "1-65535"},
		{"empty allowlist", `{"tenants": {"a": {"mode": "allowlist"}}}`, "at least one"},
		{"allow without allowlist", `{"tenants": {"a": {"mode": "none", "allow": [{"host": "x.org", "ports": [443]}]}}}`, "only applies"},
		{"unknown field", `{"tenants": {"a": {"mode": "none", "mdoe": "open"}}}`, "unknown field"},
		{"bad mode", `{"tenants": {"a": {"mode": "isolated"}}}`, "egress mode"},
		{"empty key", `{"tenants": {"": {"mode": "none"}}}`, "empty tenant key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw, Egress{})
			if err == nil {
				t.Fatalf("Parse(%s) = nil error", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Check is the startup gate: every mode this deployment cannot enforce has to be
// named, with the tenant, or the operator finds out from an incident instead of
// from the boot.
func TestCheckRefusesWhatTheDeploymentCannotEnforce(t *testing.T) {
	allowlist, err := Parse(`{"default": {"mode": "none"}, "tenants": {"bob@piforge.local": {"mode": "allowlist", "allow": [{"host": "registry.npmjs.org", "ports": [443]}]}}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = allowlist.Check(sandbox.NetworkTenant)
	if err == nil {
		t.Fatal("allowlist was accepted")
	}
	for _, want := range []string{"bob@piforge.local", "allowlist", "not implemented"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}

	// none needs a bridge of its own.
	none, err := Parse(`{"default": {"mode": "none"}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := none.Check(sandbox.NetworkTenant); err != nil {
		t.Fatalf("none on per-tenant bridges was refused: %v", err)
	}
	sharedErr := none.Check(sandbox.NetworkShared)
	if sharedErr == nil || !strings.Contains(sharedErr.Error(), "shared network") {
		t.Fatalf("none on a shared network = %v", sharedErr)
	}

	// Overrides are meaningless without per-tenant bridges, even when every mode
	// is open: the operator clearly expected per-tenant control.
	openWithOverride, err := Parse(`{"default": {"mode": "open"}, "tenants": {"bob@piforge.local": {"mode": "open"}}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := openWithOverride.Check(sandbox.NetworkShared); err == nil || !strings.Contains(err.Error(), "override") {
		t.Fatalf("per-tenant overrides on a shared network = %v", err)
	}
	// Open everywhere on a shared network is the single-host shape and is fine.
	open, err := Parse(`{"default": {"mode": "open"}}`, Egress{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := open.Check(sandbox.NetworkShared); err != nil {
		t.Fatalf("open on a shared network was refused: %v", err)
	}
	// A nil policy is "nothing configured" and has nothing to check.
	var nilPolicy *Policy
	if err := nilPolicy.Check(sandbox.NetworkShared); err != nil {
		t.Fatalf("nil policy: %v", err)
	}
}

// The file is the deployment's configuration, so a missing or unreadable one is
// an error that names the path.
func TestLoadFileNamesThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenant-egress.json")
	if _, err := LoadFile(path, Egress{}); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing file = %v", err)
	}
	body := `{"comment": "x", "default": {"mode": "none"}, "tenants": {"usr_a": {"mode": "open"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadFile(path, Egress{Mode: sandbox.EgressOpen})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if policy.Source() != path {
		t.Fatalf("Source() = %q, want the path", policy.Source())
	}
	if policy.Default().Mode != sandbox.EgressNone || policy.For("usr_a", "").Mode != sandbox.EgressOpen {
		t.Fatalf("policy = %+v", policy)
	}
}

// The shipped default is closed, and the file's own comment says so; this pins
// the shape the compose stack mounts, so a change to deploy/tenant-egress.json
// cannot quietly open every tenant.
func TestTheShippedPolicyIsClosedByDefault(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "tenant-egress.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("shipped policy not found at %s: %v", path, err)
	}
	policy, err := LoadFile(path, Egress{})
	if err != nil {
		t.Fatalf("the shipped policy does not parse: %v", err)
	}
	if policy.Default().Mode != sandbox.EgressNone {
		t.Fatalf("the shipped default is %q, want none", policy.Default().Mode)
	}
	if err := policy.Check(sandbox.NetworkTenant); err != nil {
		t.Fatalf("the shipped policy is not enforceable on the isolated shape: %v", err)
	}
}
