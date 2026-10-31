package tenantlimits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The default is what a tenant with no entry gets, and it has to survive an
// override that only names one field: a policy that says "bob gets more CPUs"
// must not silently remove bob's memory and process limits.
func TestForMergesPartialOverrideOverTheDefault(t *testing.T) {
	policy, err := Parse(`{
		"default": {"cpus": "1", "memory": "2g", "pids": 512, "max_sandboxes": 3},
		"tenants": {"bob@piforge.local": {"cpus": "4"}}
	}`, Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	bob := policy.For("usr_bob", "Bob@PiForge.local")
	if bob.CPUs != "4" || bob.Memory != "2g" || bob.Pids != 512 || bob.MaxSandboxes != 3 {
		t.Fatalf("bob = %+v, want cpus 4 and the rest from the default", bob)
	}
	// Lookup by user id when the policy names the id instead of the address.
	byID, err := Parse(`{"tenants": {"usr_carol": {"memory": "8g"}}}`, Limits{CPUs: "2", Pids: 256})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	carol := byID.For("usr_carol", "carol@piforge.local")
	if carol.Memory != "8g" || carol.CPUs != "2" || carol.Pids != 256 {
		t.Fatalf("carol = %+v", carol)
	}
	// A tenant with no entry gets the default untouched.
	other := policy.For("usr_dan", "dan@piforge.local")
	if other != policy.Default() {
		t.Fatalf("dan = %+v, want the default %+v", other, policy.Default())
	}
}

// An explicit 0 is not "inherit": it is how an operator says "no limit here".
func TestExplicitZeroOverridesTheDefault(t *testing.T) {
	policy, err := Parse(`{
		"default": {"pids": 512, "max_sandboxes": 3},
		"tenants": {"usr_x": {"pids": 0, "max_sandboxes": 0}}
	}`, Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := policy.For("usr_x", "")
	if got.Pids != 0 || got.MaxSandboxes != 0 {
		t.Fatalf("usr_x = %+v, want both limits removed", got)
	}
}

// A deployment that cannot enforce what it was told to enforce must not start,
// and the error has to name the tenant that is wrong.
func TestParseRejectsATypoOrAnImpossibleLimit(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown field", `{"default": {"cpus": "1", "memory_limt": "2g"}}`, "unknown field"},
		{"bad cpus", `{"tenants": {"bob": {"cpus": "two"}}}`, "bob"},
		{"negative pids", `{"tenants": {"bob": {"pids": -1}}}`, "pids"},
		{"bad memory", `{"tenants": {"bob": {"memory": "2x"}}}`, "memory"},
		{"empty key", `{"tenants": {"": {"cpus": "1"}}}`, "empty tenant key"},
		{"negative sandboxes", `{"default": {"max_sandboxes": -2}}`, "max_sandboxes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw, Limits{})
			if err == nil {
				t.Fatalf("Parse(%s) = nil error", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The file is the deployment's statement of its policy; a missing one has to be
// an error when a path was configured, not a silent "no limits".
func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenant-limits.json")
	if err := os.WriteFile(path, []byte(`{"default": {"cpus": "1", "memory": "1g", "pids": 128}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	policy, err := LoadFile(path, Limits{})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if policy.Source() != path {
		t.Fatalf("source = %q, want the path", policy.Source())
	}
	if got := policy.For("usr_1", ""); got.CPUs != "1" || got.Pids != 128 {
		t.Fatalf("limits = %+v", got)
	}
	if _, err := LoadFile(filepath.Join(dir, "absent.json"), Limits{}); err == nil {
		t.Fatal("a missing policy file was accepted")
	}
}

// Tenants lists the overrides so an admin screen can show them for an account
// that has no sandbox yet.
func TestTenantsIsSortedAndCaseFolded(t *testing.T) {
	policy, err := Parse(`{"tenants": {"Zoe@piforge.local": {"cpus": "2"}, "amy@piforge.local": {"cpus": "1"}}}`, Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := policy.Tenants()
	if len(got) != 2 || got[0] != "amy@piforge.local" || got[1] != "zoe@piforge.local" {
		t.Fatalf("tenants = %v", got)
	}
}
