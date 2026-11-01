package urlguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixedLookup is the DNS half of every test: the policy's answer depends on
// what the name resolves to, so a table of answers is the fixture.
func fixedLookup(answers map[string][]string, err error) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if err != nil {
			return nil, err
		}
		raw, ok := answers[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		out := make([]netip.Addr, 0, len(raw))
		for _, item := range raw {
			addr, perr := netip.ParseAddr(item)
			if perr != nil {
				return nil, perr
			}
			out = append(out, addr)
		}
		return out, nil
	}
}

func policyWith(t *testing.T, allow []Allow, answers map[string][]string) *Policy {
	t.Helper()
	policy, err := New(allow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policy.lookup = fixedLookup(answers, nil)
	return policy
}

// TestRefusesEveryAddressTheReportNames is the core table: the two probes from
// adversarial acceptance #4, the metadata address, and the neighbouring ranges.
// Every one must be refused with the reason the report asked for, and the
// refusal must name the host.
func TestRefusesEveryAddressTheReportNames(t *testing.T) {
	policy := policyWith(t, nil, map[string][]string{
		"piforge":            {"172.26.0.2"},
		"a4marker":           {"172.26.0.5"},
		"internal.example":   {"10.1.2.3"},
		"rebind.example.com": {"93.184.216.34"},
	})

	cases := []struct {
		url    string
		reason string
	}{
		// The report's probe 1: the control plane's own loopback, by the
		// compose service name.
		{"http://piforge:7700/healthz", ReasonName},
		// The report's probe 1b: a marker container only the control plane can
		// reach. The name is not in the deny list, so the *address* decides.
		{"http://a4marker:9021/mcp", ReasonPrivate},
		{"http://127.0.0.1:7700/v1/admin/status", ReasonLoopback},
		{"http://localhost:7700/healthz", ReasonLoopback},
		{"http://[::1]:7700/healthz", ReasonLoopback},
		{"http://169.254.169.254/latest/meta-data/", ReasonLinkLocal},
		{"http://[fe80::1]/", ReasonLinkLocal},
		{"http://10.0.0.1/", ReasonPrivate},
		{"http://172.16.0.1/", ReasonPrivate},
		{"http://172.31.255.254/", ReasonPrivate},
		{"http://192.168.1.1/", ReasonPrivate},
		{"http://[fc00::1]/", ReasonUniqueLocal},
		{"http://[fd12:3456::1]/", ReasonUniqueLocal},
		{"http://0.0.0.0:8080/", ReasonUnspecified},
		{"http://[::]:8080/", ReasonUnspecified},
		{"http://224.0.0.1/", ReasonMulticast},
		{"http://[ff02::1]/", ReasonMulticast},
		{"http://100.64.0.1/", ReasonCGNAT},
		{"http://240.0.0.1/", ReasonReserved},
		{"http://255.255.255.255/", ReasonReserved},
		{"http://192.0.2.10/", ReasonDocumentation},
		{"http://198.18.0.1/", ReasonBenchmark},
		{"http://0.1.2.3/", ReasonThisNetwork},
		{"http://host.docker.internal:9021/mcp", ReasonName},
		{"http://gateway.docker.internal/", ReasonName},
		{"http://mcp.internal/mcp", ReasonName},
		{"http://printer.local/", ReasonName},
		{"http://internal.example/", ReasonPrivate},
		// A name that resolves public at save time can resolve private later:
		// the same Check catches the second answer, which is why the dialer
		// calls it again.
		{"http://rebind.example.com/", ""},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			_, err := policy.Check(context.Background(), tc.url)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("Check(%q) = %v, want public and allowed", tc.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check(%q) was allowed; want a %s refusal", tc.url, tc.reason)
			}
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Check(%q) = %T %v; want *Refusal", tc.url, err, err)
			}
			if refusal.Reason != tc.reason {
				t.Fatalf("Check(%q) reason = %q (%s), want %q", tc.url, refusal.Reason, refusal.Detail, tc.reason)
			}
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) does not match ErrRefused", tc.url)
			}
			if refusal.Detail == "" || !strings.Contains(refusal.Detail, refusal.Host) {
				t.Fatalf("refusal detail %q does not name the host %q", refusal.Detail, refusal.Host)
			}
		})
	}
}

// TestOwnContainerNameIsRefused: the control plane's own hostname is what
// docker's embedded DNS answers for "this container", and it is not in any
// static list because it changes with every deployment.
func TestOwnContainerNameIsRefused(t *testing.T) {
	policy := policyWith(t, nil, map[string][]string{"6508ba93a0d1": {"172.26.0.2"}})
	policy.self = "6508ba93a0d1"
	_, err := policy.Check(context.Background(), "http://6508ba93a0d1:7700/healthz")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonName {
		t.Fatalf("own hostname = %v, want a host-name refusal", err)
	}
}

func TestRefusesSchemesAndShapes(t *testing.T) {
	policy := policyWith(t, nil, nil)
	cases := map[string]string{
		"file:///data/master.key":    ReasonScheme,
		"gopher://127.0.0.1:70/_":    ReasonScheme,
		"unix:///var/run/docker":     ReasonScheme,
		"ftp://example.com/x":        ReasonScheme,
		"javascript:alert(1)":        ReasonScheme,
		"//example.com/no-scheme":    ReasonScheme,
		"http:///no-host":            ReasonNoHost,
		"http://user:pw@example.com": ReasonCredentials,
		"http://example.com:0/":      ReasonPort,
		"http://example.com:70000/":  ReasonPort,
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := policy.Check(context.Background(), raw)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Check(%q) = %v; want a refusal", raw, err)
			}
			if refusal.Reason != want {
				t.Fatalf("Check(%q) reason = %q, want %q", raw, refusal.Reason, want)
			}
		})
	}
}

// TestPublicAddressesAreAllowed is the other half: the policy must not refuse
// the providers and MCP servers a deployment actually uses.
func TestPublicAddressesAreAllowed(t *testing.T) {
	policy := policyWith(t, nil, map[string][]string{
		"api.anthropic.com": {"160.79.104.10"},
		"api.openai.com":    {"104.18.33.45", "2606:4700::6812:212d"},
		"mcp.example.com":   {"93.184.216.34"},
		"api.example.com": {"121.37.0.1"},
	})
	for _, raw := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://api.openai.com/v1/chat/completions",
		"https://mcp.example.com/mcp",
		"http://api.example.com/v1",
		"https://93.184.216.34/", // a public literal needs no DNS
	} {
		target, err := policy.Check(context.Background(), raw)
		if err != nil {
			t.Fatalf("Check(%q) = %v, want allowed", raw, err)
		}
		if target.Host == "" || target.Port == "" {
			t.Fatalf("Check(%q) returned an incomplete target %+v", raw, target)
		}
	}
}

// TestDNSFailureIsARefusalAtCallTimeAndNotAtSaveTime: an unresolvable name is
// refused when dialling (fail closed) but allowed to be *stored*, so a provider
// whose DNS is momentarily down is still editable. The boundary is unchanged:
// the call is what reaches the network.
func TestDNSFailureIsARefusalAtCallTimeAndNotAtSaveTime(t *testing.T) {
	policy, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policy.lookup = fixedLookup(nil, &net.DNSError{Err: "no such host", IsNotFound: true})
	_, err = policy.Check(context.Background(), "https://example.invalid/v1")
	if err == nil {
		t.Fatal("Check allowed an unresolvable host; want a dns refusal")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonDNS {
		t.Fatalf("Check = %v, want a dns refusal", err)
	}
	if _, err := policy.CheckSave(context.Background(), "https://example.invalid/v1"); err != nil {
		t.Fatalf("CheckSave refused an unresolvable host: %v", err)
	}
	// Everything decidable without DNS is still refused at save time.
	if _, err := policy.CheckSave(context.Background(), "http://127.0.0.1:7700/"); err == nil {
		t.Fatal("CheckSave allowed loopback")
	}
	if _, err := policy.CheckSave(context.Background(), "file:///data"); err == nil {
		t.Fatal("CheckSave allowed file://")
	}
}

// TestAllowListIsTheOnlyWayToWiden is the deployment rule: an operator's file
// entry is the one way a non-public target becomes dialable, it is scoped to
// the host (or the CIDR) and the port, and it does not widen anything else.
func TestAllowListIsTheOnlyWayToWiden(t *testing.T) {
	policy := policyWith(t, []Allow{
		{Host: "piforge", Ports: []int{7700}},
		{Host: "mcp.internal.example.com", Ports: []int{443}},
		{CIDR: "10.20.0.0/16", Ports: []int{8443}},
	}, map[string][]string{
		"mcp.internal.example.com": {"10.20.4.5"},
		"other.example.com":        {"10.21.4.6"},
	})

	allowed := []string{
		"http://piforge:7700/gw/mcp/token",          // the host entry, exact port
		"https://mcp.internal.example.com/mcp",      // the host entry over 443
		"http://10.20.4.5:8443/mcp",                 // the CIDR entry
		"http://mcp.internal.example.com:443/other", // the host, explicit 443
	}
	for _, raw := range allowed {
		if _, err := policy.Check(context.Background(), raw); err != nil {
			t.Fatalf("allow-list did not allow %q: %v", raw, err)
		}
	}

	refused := []string{
		"http://piforge:7701/",                   // wrong port
		"http://10.20.4.5:8444/",                 // CIDR, wrong port
		"http://other.example.com:8443/",         // outside the CIDR
		"http://127.0.0.1:7700/",                 // a different loopback target
		"https://mcp.internal.example.com:9443/", // the host entry, on a port no entry names
	}
	for _, raw := range refused {
		if _, err := policy.Check(context.Background(), raw); err == nil {
			t.Fatalf("allow-list widened %q; it must be scoped to the host/CIDR and port", raw)
		}
	}
}

func TestAllowEntriesAreValidated(t *testing.T) {
	cases := []Allow{
		{}, // neither host nor cidr
		{Host: "a.example", CIDR: "10.0.0.0/8", Ports: []int{443}}, // both
		{Host: "https://a.example", Ports: []int{443}},             // scheme
		{Host: "*.example.com", Ports: []int{443}},                 // wildcard
		{Host: "a.example", Ports: []int{0}},                       // port range
		{CIDR: "not-a-cidr", Ports: []int{443}},
	}
	for _, entry := range cases {
		if _, err := New([]Allow{entry}); err == nil {
			t.Fatalf("New accepted %+v", entry)
		}
	}
	// The file the operator writes is stricter than the programmatic
	// constructor: a destination with no port list is refused, because "any
	// port on this host" is a much larger grant than it reads as.
	if _, err := Parse(`{"allow": [{"host": "mcp.internal.example.com"}]}`); err == nil {
		t.Fatal("Parse accepted an entry with no ports")
	}
	if _, err := New([]Allow{{Host: "127.0.0.1"}}); err != nil {
		t.Fatalf("New without ports (a test server's ephemeral port) = %v", err)
	}
}

func TestParseRejectsUnknownFieldsAndAcceptsTheDocumentedShape(t *testing.T) {
	policy, err := Parse(`{
		"comment": "one internal MCP server this operator runs",
		"allow": [{"host": "mcp.internal.example.com", "ports": [443]}]
	}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if policy.Entries() != 1 {
		t.Fatalf("entries = %d, want 1", policy.Entries())
	}
	if _, err := Parse(`{"allow": [], "allowlist": []}`); err == nil {
		t.Fatal("Parse accepted an unknown field")
	}
}

// TestDialRefusesAndPins is the rebinding half: the dialer resolves, verifies
// and connects to the verified address, so the policy cannot be bypassed by
// skipping Check, and an allowed name still connects.
func TestDialRefusesAndPins(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, aerr := listener.Accept()
			if aerr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	closed, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn, err := closed.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		_ = conn.Close()
		t.Fatal("the closed policy dialled loopback")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonLoopback {
		t.Fatalf("DialContext = %v; want a loopback refusal", err)
	}

	// The allow-list is what makes the same dial work.
	open, err := New([]Allow{{Host: "127.0.0.1", Ports: []int{port}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn, err = open.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("allowed dial failed: %v", err)
	}
	_ = conn.Close()
}

// TestRedirectToARefusedTargetIsRefused: an allowed host that answers 302 to
// the metadata service must not be followed. The first hop succeeded, so this
// is the check that a redirect is re-validated rather than trusted.
func TestRedirectToARefusedTargetIsRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer upstream.Close()

	addr := upstream.Listener.Addr().(*net.TCPAddr)
	policy, err := New([]Allow{{CIDR: "127.0.0.1/32", Ports: []int{addr.Port}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// httptest.NewServer listens on 127.0.0.1, which the policy refuses by
	// default; the allow entry above is the operator's exception.
	client := policy.Client(3 * time.Second)
	resp, err := client.Get(upstream.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the client followed a redirect to 169.254.169.254")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonLinkLocal {
		t.Fatalf("redirect = %v; want a link-local refusal", err)
	}
}

// TestClientDoesNotUseAnEnvironmentProxy: HTTP_PROXY would be a second dialer
// the policy does not see, so the transport must not read it.
func TestClientDoesNotUseAnEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	policy, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if policy.Transport().Proxy != nil {
		t.Fatal("the transport reads a proxy from the environment")
	}
}
