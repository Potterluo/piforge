package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The whole of "none" is `docker network create --internal`: no default route,
// no NAT. This pins the argv, because a bridge created without the flag looks
// exactly like one that has it until an agent fetches something.
func TestTenantNetworkCreateArgsPinTheInternalFlag(t *testing.T) {
	closed := tenantNetworkCreateArgs("piforge-t-abc", EgressNone)
	if !containsArg(closed, "--internal") {
		t.Fatalf("a closed tenant network was created without --internal: %v", closed)
	}
	assertArgsContain(t, closed, "--label", "piforge.tenant=piforge-t-abc")
	// The mode is a label as well as policy, so `docker network ls --filter
	// label=piforge.egress=none` answers "who is closed" from the daemon.
	assertArgsContain(t, closed, "--label", "piforge.egress=none")
	if closed[len(closed)-1] != "piforge-t-abc" {
		t.Fatalf("the network name is not the last argument: %v", closed)
	}

	open := tenantNetworkCreateArgs("piforge-t-abc", EgressOpen)
	for _, arg := range open {
		if arg == "--internal" {
			t.Fatalf("an open tenant network was created internal: %v", open)
		}
	}
	assertArgsContain(t, open, "--label", "piforge.egress=open")

	// allowlist is refused before a create is attempted, but if it ever reached
	// this function it must not be treated as open.
	if args := tenantNetworkCreateArgs("n", EgressAllowlist); !containsArg(args, "--internal") {
		t.Fatalf("allowlist would have produced an open bridge: %v", args)
	}
}

// The three shapes and the mistake they must never make.
func TestParseEgressMode(t *testing.T) {
	cases := map[string]EgressMode{
		"":          EgressNone,
		"none":      EgressNone,
		"NONE":      EgressNone,
		" none ":    EgressNone,
		"open":      EgressOpen,
		"allowlist": EgressAllowlist,
	}
	for raw, want := range cases {
		got, err := ParseEgressMode(raw)
		if err != nil {
			t.Fatalf("ParseEgressMode(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseEgressMode(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseEgressMode("isolated"); err == nil || !strings.Contains(err.Error(), "none") {
		t.Fatalf("an unknown mode was accepted: %v", err)
	}
}

// allowlist is the one mode that must be refused loudly: it is the mode an
// operator would most reasonably believe they had configured.
func TestAllowlistIsRefusedAndTheRefusalSaysWhy(t *testing.T) {
	err := EgressAllowlist.Enforceable()
	if err == nil {
		t.Fatal("allowlist was accepted")
	}
	for _, want := range []string{"allowlist", "not implemented", "proxy", "iptables"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
	if !errors.Is(err, ErrEgressUnenforceable) {
		t.Fatalf("the refusal is not ErrEgressUnenforceable: %v", err)
	}
	if err := EgressNone.Enforceable(); err != nil {
		t.Fatalf("none refused: %v", err)
	}
	if err := EgressOpen.Enforceable(); err != nil {
		t.Fatalf("open refused: %v", err)
	}
	if EgressNone.Closed() != true || !EgressAllowlist.Closed() || EgressOpen.Closed() {
		t.Fatal("Closed() disagrees with the modes")
	}
}

// egressFor is where "nobody configured a policy" and "the policy answered
// nothing" are told apart. Getting it backwards opens the network.
func TestEgressForDefaultsToOpenOnlyWhenNothingWasConfigured(t *testing.T) {
	bare := &Docker{cfg: DockerConfig{}}
	if got := bare.egressFor(context.Background(), "usr_a"); got != EgressOpen {
		t.Fatalf("no policy at all = %q, want the historical open", got)
	}

	// A policy that returns an empty mode must close: it is a policy, and its
	// silence is not permission.
	emptyAnswer := &Docker{cfg: DockerConfig{
		EgressFor: func(context.Context, string) EgressMode { return "" },
	}}
	if got := emptyAnswer.egressFor(context.Background(), "usr_a"); got != EgressNone {
		t.Fatalf("an unanswered policy = %q, want none", got)
	}

	// A typo in an answer closes too.
	typo := &Docker{cfg: DockerConfig{
		EgressFor: func(context.Context, string) EgressMode { return "openn" },
	}}
	if got := typo.egressFor(context.Background(), "usr_a"); got != EgressNone {
		t.Fatalf("an unparsable policy answer = %q, want none", got)
	}

	perTenant := &Docker{cfg: DockerConfig{
		Egress: EgressNone,
		EgressFor: func(_ context.Context, userID string) EgressMode {
			if userID == "usr_open" {
				return EgressOpen
			}
			return EgressNone
		},
	}}
	if got := perTenant.egressFor(context.Background(), "usr_open"); got != EgressOpen {
		t.Fatalf("usr_open = %q", got)
	}
	if got := perTenant.egressFor(context.Background(), "usr_closed"); got != EgressNone {
		t.Fatalf("usr_closed = %q", got)
	}

	// A configured deployment-wide mode is honoured when there is no function.
	if got := (&Docker{cfg: DockerConfig{Egress: EgressOpen}}).egressFor(context.Background(), "x"); got != EgressOpen {
		t.Fatalf("deployment-wide open = %q", got)
	}
}

// The shared shape cannot hold a per-tenant boundary, so a create that would ask
// for one has to fail instead of producing an ordinary bridge.
func TestEgressNoneOnASharedNetworkIsRefused(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{Network: "piforge-sandboxes", NetworkMode: NetworkShared, Egress: EgressNone}}
	err := provider.ensureNetwork(context.Background(), "usr_a")
	if err == nil || !strings.Contains(err.Error(), "shared network") {
		t.Fatalf("none on a shared network = %v", err)
	}
	if !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("the refusal does not name the fix: %v", err)
	}
	// Open is the single-host shape and passes through without touching Docker.
	if err := (&Docker{cfg: DockerConfig{Network: "piforge-sandboxes", Egress: EgressOpen}}).
		ensureNetwork(context.Background(), "usr_a"); err != nil {
		t.Fatalf("open on a shared network: %v", err)
	}
	// allowlist is refused before any network question is asked.
	err = (&Docker{cfg: DockerConfig{NetworkMode: NetworkTenant, Egress: EgressAllowlist}}).
		ensureNetwork(context.Background(), "usr_a")
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("allowlist at create time = %v", err)
	}
}

// The mismatch check is the difference between "the policy changed" and "the
// sandbox is still open under a page that says closed". Only an exact "true" may
// read as internal: a Docker whose output format changed must fail the create,
// not silently agree.
func TestDockerBoolIsStrict(t *testing.T) {
	yes := []string{"true", "True", " true\n", "TRUE "}
	for _, raw := range yes {
		if !dockerBool(raw) {
			t.Fatalf("dockerBool(%q) = false", raw)
		}
	}
	no := []string{"false", "False", "", "1", "yes", "true false"}
	for _, raw := range no {
		if dockerBool(raw) {
			t.Fatalf("dockerBool(%q) = true", raw)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
