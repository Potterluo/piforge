// Package urlguard is the one policy for every URL this control plane fetches
// on a tenant's behalf.
//
// It exists because adversarial acceptance #4 measured two open fetchers
// (e2e/.artifacts/adversarial4/REPORT.md, attacks 18 and 21):
//
//	POST /v1/mcp/servers  stored a tenant URL with no scheme or host policy, and
//	                      the broker then reverse-proxied the sandbox's request
//	                      to it from the control plane's network position:
//	                      http://piforge:7700/healthz answered the control
//	                      plane's own loopback, and a marker container reachable
//	                      only from the control plane answered "you_are: 172.26.0.2".
//	POST /v1/providers   + /test made the control plane POST to a tenant URL and
//	                      echoed part of a non-2xx body back.
//
// Both are the same defect one level up from the read path: the control plane
// has a different network position from the sandbox (internet, the docker host
// gateway, its own loopback, every sibling container), and a tenant-supplied URL
// was the way to borrow it. The fix is not two checks in two handlers; it is one
// policy, applied at every fetch site, twice: when the URL is saved (a typo or an
// obvious attack is refused with a sentence) and again when it is dialled.
//
// The rule, stated once:
//
//   - the scheme must be http or https (file:, gopher:, unix: and an empty
//     scheme are refused — the broker's http.DefaultTransport does not speak
//     them either, but a refusal that names the scheme is a bug report, not a
//     dial error);
//   - the host must not be a name that only ever means "this machine"
//     (localhost, host.docker.internal, the compose service name, this
//     container's own hostname, a *.internal / *.local name);
//   - every address the host resolves to must be public: loopback, link-local
//     (169.254/16, which is where cloud metadata lives), private (10/8,
//     172.16/12, 192.168/16), unique-local (fc00::/7), unspecified, multicast,
//     CGNAT, documentation and reserved ranges are refused;
//   - the check is repeated at dial time by this package's DialContext, which
//     resolves, verifies and then dials the verified IP. A name that resolved to
//     a public address when it was saved and to a private one a minute later
//     (DNS rebinding) therefore still fails, and the connection goes to the
//     address that was verified, not to a second lookup's answer;
//   - a redirect is re-validated hop by hop (http.Client.CheckRedirect). Nothing
//     follows a 302 to 169.254.169.254;
//   - a refusal is a typed *Refusal naming the reason code, the host and an
//     English sentence, and the caller turns it into a 400.
//
// The only way to reach a non-public target is an explicit allow entry, written
// by the operator in a file (see LoadFile), default empty. The allow-list can
// only widen the set of dialable targets — it is a deployment rule, not a
// tenant setting — because the alternative ("allow it because an admin saved
// it") is exactly the hole this package closes.
package urlguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Reason codes. They are stable strings on the wire ("details.reason"), so a
// client can branch on them and an operator can grep a log line.
const (
	ReasonParse         = "parse"
	ReasonScheme        = "scheme"
	ReasonNoHost        = "no-host"
	ReasonCredentials   = "credentials"
	ReasonPort          = "port"
	ReasonName          = "host-name"
	ReasonDNS           = "dns"
	ReasonUnspecified   = "unspecified"
	ReasonLoopback      = "loopback"
	ReasonLinkLocal     = "link-local"
	ReasonMulticast     = "multicast"
	ReasonUniqueLocal   = "unique-local"
	ReasonPrivate       = "private"
	ReasonThisNetwork   = "this-network"
	ReasonCGNAT         = "cgnat"
	ReasonDocumentation = "documentation"
	ReasonBenchmark     = "benchmark"
	ReasonReserved      = "reserved"
)

// Refusal is the typed answer to "may the control plane dial this?".
//
// It carries the host and the reason as data, because both the 400 body and the
// log line name them and neither may be recovered from a formatted string.
type Refusal struct {
	// Host is the host as it was written (lowercased, no trailing dot). Empty
	// only for a URL that has no host at all.
	Host string
	// Reason is one of the Reason* codes.
	Reason string
	// Detail is the sentence a person reads. It names the host and the reason
	// and never contains a credential: a base_url with userinfo is refused
	// before the URL reaches here.
	Detail string
}

func (r *Refusal) Error() string { return "urlguard: " + r.Detail }

// Is makes a refusal match urlguard.ErrRefused, so a caller that only wants
// "was this the policy?" can ask errors.Is and one that wants the reason can
// ask errors.As.
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// ErrRefused is the sentinel every *Refusal matches.
var ErrRefused = errors.New("urlguard: refused by the outbound URL policy")

func refuse(host, reason, format string, args ...any) *Refusal {
	return &Refusal{Host: host, Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Allow is one exception an operator wrote down: a host name or a CIDR, on the
// ports named. Exactly one of Host and CIDR is set.
//
// A port list is required in the configuration file for the same reason
// tenantegress requires one: "any port on this host" is a much larger grant
// than it reads as. A programmatic entry (New) may omit Ports, which means any
// port; that exists for test servers and hand-built fixtures, and the file the
// operator writes is always parsed with the strict rule (see Parse).
type Allow struct {
	// Host is a bare DNS name (no scheme, no path, no wildcard). It is compared
	// case-insensitively against the requested host, before DNS: that is what
	// makes it able to allow a name like "mcp.internal.example.com" whose
	// addresses this deployment would otherwise refuse.
	Host string `json:"host,omitempty"`
	// CIDR is a network, e.g. "10.20.0.0/16", matched against the resolved
	// address.
	CIDR string `json:"cidr,omitempty"`
	// Ports are the TCP ports this destination may be reached on.
	Ports []int `json:"ports,omitempty"`
}

func (a Allow) validate(requirePorts bool) error {
	host := strings.TrimSpace(a.Host)
	cidr := strings.TrimSpace(a.CIDR)
	switch {
	case host == "" && cidr == "":
		return fmt.Errorf(`needs a "host" or a "cidr"`)
	case host != "" && cidr != "":
		return fmt.Errorf(`sets both "host" (%q) and "cidr" (%q); one destination per entry`, host, cidr)
	}
	if host != "" {
		if strings.Contains(host, "://") || strings.ContainsAny(host, " /") {
			return fmt.Errorf(`host %q must be a bare name (no scheme, no path)`, host)
		}
		if strings.Contains(host, "*") {
			return fmt.Errorf(`host %q: wildcards cannot be enforced, name the hosts`, host)
		}
	}
	if cidr != "" {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("cidr %q: %w", cidr, err)
		}
	}
	if len(a.Ports) == 0 && requirePorts {
		return fmt.Errorf("no ports; name the TCP ports this destination may be reached on (e.g. [443])")
	}
	for _, port := range a.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("port %d is outside 1-65535", port)
		}
	}
	return nil
}

// allowEntry is a validated Allow with its CIDR parsed once.
type allowEntry struct {
	host   string
	prefix netip.Prefix
	cidr   bool
	ports  []int
}

// Policy is the resolved rule. The zero value is not usable; use Default,
// New, Parse or LoadFile.
type Policy struct {
	entries []allowEntry
	// self is this process's hostname (the control plane container's id in a
	// compose deployment). Dialling it by name would reach the control plane
	// itself, which is never a tenant's MCP server.
	self string
	// lookup is net.DefaultResolver unless a test injects one.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Default is the deployment rule: public http(s) only, no exceptions.
func Default() *Policy {
	return &Policy{self: ownHostname(), lookup: defaultLookup}
}

// New builds a policy with explicit allow entries.
//
// This is the programmatic constructor: an entry may omit Ports (any port), and
// a test server that listens on an ephemeral port has no other way to name it.
// A deployment's file goes through Parse, which requires the ports.
func New(allow []Allow) (*Policy, error) {
	p := Default()
	if err := p.setAllow(allow, false); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Policy) setAllow(allow []Allow, requirePorts bool) error {
	for i, entry := range allow {
		if err := entry.validate(requirePorts); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
		resolved := allowEntry{
			host:  strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry.Host), ".")),
			ports: append([]int(nil), entry.Ports...),
		}
		if strings.TrimSpace(entry.CIDR) != "" {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(entry.CIDR))
			if err != nil {
				return fmt.Errorf("allow[%d]: cidr %q: %w", i, entry.CIDR, err)
			}
			resolved.cidr = true
			resolved.prefix = prefix.Masked()
		}
		p.entries = append(p.entries, resolved)
	}
	return nil
}

// Describes renders the policy for a boot log line or an admin page: the rule
// plus how many exceptions an operator wrote, never the host list itself.
func (p *Policy) Describes() string {
	if p == nil || len(p.entries) == 0 {
		return "public http(s) only"
	}
	return fmt.Sprintf("public http(s) only + %d allow entries", len(p.entries))
}

// Entries reports how many allow entries the policy holds.
func (p *Policy) Entries() int {
	if p == nil {
		return 0
	}
	return len(p.entries)
}

// Target is a URL the policy approved.
type Target struct {
	URL  *url.URL
	Host string
	Port string
	// Addrs are the verified addresses. They are what a caller that does its own
	// dialing (git, via http.curloptResolve) must pin; the HTTP transport in this
	// package resolves again on purpose, so that a rebind between the check and
	// the connection is caught rather than assumed away.
	Addrs []netip.Addr
}

// Check is the request-time question: may this URL be dialled, right now?
//
// It parses, applies the scheme/host rules, resolves the name, and refuses if
// any resolved address is not public. It is deliberately a refusal and not a
// warning: a control plane that believes a URL is public while it is dialling
// the operator's host is worse off than one whose request failed with a
// sentence.
func (p *Policy) Check(ctx context.Context, raw string) (*Target, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, refuse("", ReasonParse, "%q is not a URL: %v", redact(raw), err)
	}
	if u.Opaque != "" {
		return nil, refuse("", ReasonScheme, "%q is not a hierarchical URL; only http(s)://host/path is fetched", redact(raw))
	}
	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "http", "https":
	case "":
		return nil, refuse("", ReasonScheme, "%q has no scheme; only http and https are fetched", redact(raw))
	default:
		return nil, refuse("", ReasonScheme,
			"scheme %q is refused: this deployment fetches only http and https (not file:, gopher:, unix:)", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, refuse("", ReasonNoHost, "%q has no host", redact(raw))
	}
	if u.User != nil {
		return nil, refuse(host, ReasonCredentials,
			"the URL embeds credentials for %q; this deployment refuses a URL with userinfo (store the credential as a field)", host)
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return nil, refuse(host, ReasonPort, "port %q is outside 1-65535", port)
	}
	addrs, err := p.verify(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return &Target{URL: u, Host: host, Port: port, Addrs: addrs}, nil
}

// CheckSave is Check for a path that stores a URL rather than dialling it.
//
// The one difference: a name this process cannot resolve right now is accepted.
// A provider whose DNS is momentarily down must still be editable, and the
// boundary is not weakened by that — Check runs again on every call, where an
// unresolvable name is a refusal. Everything the policy can decide without DNS
// (scheme, credentials, localhost / docker / compose names) is still refused
// here, which is where an attacker's first attempt lands.
func (p *Policy) CheckSave(ctx context.Context, raw string) (*Target, error) {
	target, err := p.Check(ctx, raw)
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal.Reason == ReasonDNS {
		return nil, nil
	}
	return target, err
}

// verify is the shared half: name rules, then every address, with the allow
// list able to say yes where the classifier says no.
func (p *Policy) verify(ctx context.Context, host, port string) ([]netip.Addr, error) {
	if p.allowedHost(host, port) {
		// An operator wrote this name down. A literal is already the address;
		// a name is resolved so the caller can pin it, but not refused when it
		// is unresolvable *here*: an allowed internal server may only exist
		// from another network position.
		if addr, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{addr.Unmap()}, nil
		}
		addrs, err := p.lookupAddrs(ctx, host)
		if err != nil {
			return nil, nil
		}
		return addrs, nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if err := p.checkAddr(host, port, addr); err != nil {
			return nil, err
		}
		return []netip.Addr{addr}, nil
	}
	if reason := p.refuseName(host); reason != "" {
		return nil, refuse(host, reason,
			"host %q is refused: it is a name that only means this machine or the docker host "+
				"(localhost / host.docker.internal / the compose service / this container), and the control "+
				"plane's own network position is not a tenant's to borrow", host)
	}
	addrs, err := p.lookupAddrs(ctx, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no addresses")
		}
		return nil, refuse(host, ReasonDNS, "host %q could not be resolved: %v", host, err)
	}
	for _, addr := range addrs {
		if err := p.checkAddr(host, port, addr.Unmap()); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

func (p *Policy) lookupAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	lookup := p.lookup
	if lookup == nil {
		lookup = defaultLookup
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.Unmap())
	}
	return out, nil
}

func (p *Policy) checkAddr(host, port string, addr netip.Addr) error {
	if p.allowedAddr(addr, port) {
		return nil
	}
	reason, phrase := classify(addr)
	if reason == "" {
		return nil
	}
	shown := host
	if addr.String() != host {
		shown = host + " (" + addr.String() + ")"
	}
	return refuse(host, reason,
		"host %q is refused: %s is %s, and this deployment fetches only public http(s) addresses "+
			"(loopback, link-local 169.254/16, private, unique-local, unspecified and multicast are refused; "+
			"an operator can allow one with the URL allow-list config)", shown, addr.String(), phrase)
}

// refuseName answers for the names that never need DNS: they are the control
// plane itself, the machine that runs it, or a namespace that is private by
// definition.
func (p *Policy) refuseName(host string) string {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ReasonLoopback
	}
	for _, suffix := range []string{".local", ".internal", ".localdomain", ".home.arpa", ".lan"} {
		if strings.HasSuffix(host, suffix) {
			return ReasonName
		}
	}
	for _, name := range deniedNames {
		if host == name {
			return ReasonName
		}
	}
	if p.self != "" && host == p.self {
		return ReasonName
	}
	return ""
}

// deniedNames are the single-label and docker-specific names that mean "a
// machine on this docker network" in every deployment this product ships.
// A name here is refused even when it resolves to a public address: DNS is not
// the only thing that can make it useful, and the rule has to be readable.
var deniedNames = []string{
	"piforge",
	"piforge-control",
	"sandbox-runner",
	"host.docker.internal",
	"gateway.docker.internal",
	"host.containers.internal",
	"docker.for.mac.host.internal",
	"docker.for.mac.localhost",
	"docker.for.win.localhost",
	"metadata",
	"metadata.google.internal",
	"instance-data",
	"kubernetes.default",
	"kubernetes.default.svc",
}

func (p *Policy) allowedHost(host, port string) bool {
	for _, entry := range p.entries {
		if entry.cidr || entry.host == "" {
			continue
		}
		if entry.host == host && portListed(entry.ports, port) {
			return true
		}
	}
	return false
}

func (p *Policy) allowedAddr(addr netip.Addr, port string) bool {
	for _, entry := range p.entries {
		if !entry.cidr {
			continue
		}
		if entry.prefix.Contains(addr) && portListed(entry.ports, port) {
			return true
		}
	}
	return false
}

func portListed(ports []int, port string) bool {
	if len(ports) == 0 {
		// Only a programmatic entry can get here: Parse requires a port list.
		return true
	}
	want, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	for _, candidate := range ports {
		if candidate == want {
			return true
		}
	}
	return false
}

// specialV4 are the IPv4 ranges that are not "private" by RFC 1918 but are just
// as useless to reach from a control plane, and just as interesting to an
// attacker (0/8 reaches this host on Linux, 240/4 includes broadcast).
var specialV4 = []struct {
	prefix netip.Prefix
	reason string
	phrase string
}{
	{mustPrefix("0.0.0.0/8"), ReasonThisNetwork, `a "this network" (0.0.0.0/8) address`},
	{mustPrefix("100.64.0.0/10"), ReasonCGNAT, "a carrier-grade NAT (100.64.0.0/10) address"},
	{mustPrefix("192.0.0.0/24"), ReasonReserved, "an IETF protocol assignment (192.0.0.0/24) address"},
	{mustPrefix("192.0.2.0/24"), ReasonDocumentation, "a documentation (TEST-NET-1) address"},
	{mustPrefix("198.18.0.0/15"), ReasonBenchmark, "a benchmarking (198.18.0.0/15) address"},
	{mustPrefix("198.51.100.0/24"), ReasonDocumentation, "a documentation (TEST-NET-2) address"},
	{mustPrefix("203.0.113.0/24"), ReasonDocumentation, "a documentation (TEST-NET-3) address"},
	{mustPrefix("240.0.0.0/4"), ReasonReserved, "a reserved (240.0.0.0/4) address"},
	{mustPrefix("2001:db8::/32"), ReasonDocumentation, "a documentation (2001:db8::/32) address"},
}

var uniqueLocal = mustPrefix("fc00::/7")

func mustPrefix(raw string) netip.Prefix {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		panic("urlguard: bad prefix " + raw + ": " + err.Error())
	}
	return prefix
}

// classify names the range an address falls in, or "" when it is public.
//
// The order matters: loopback and link-local before private (169.254.169.254 is
// link-local, not private, and it is the one an attacker reaches for first),
// unique-local before netip's IsPrivate (which folds fc00::/7 in with RFC 1918
// and would lose the name the report asked for).
func classify(addr netip.Addr) (reason, phrase string) {
	if !addr.IsValid() {
		return ReasonParse, "not a valid address"
	}
	switch {
	case addr.IsUnspecified():
		return ReasonUnspecified, "the unspecified address"
	case addr.IsLoopback():
		return ReasonLoopback, "a loopback address"
	case addr.IsLinkLocalUnicast():
		return ReasonLinkLocal, "a link-local address (169.254/16 is where cloud metadata lives)"
	case addr.IsMulticast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast():
		return ReasonMulticast, "a multicast address"
	}
	if addr.Is6() && uniqueLocal.Contains(addr) {
		return ReasonUniqueLocal, "a unique-local (fc00::/7) address"
	}
	if addr.IsPrivate() {
		return ReasonPrivate, "a private (RFC 1918) address"
	}
	for _, entry := range specialV4 {
		if entry.prefix.Contains(addr) {
			return entry.reason, entry.phrase
		}
	}
	if !addr.IsGlobalUnicast() {
		return ReasonReserved, "not a global unicast address"
	}
	return "", ""
}

// Transport is the dialer every fetch site must use. Its only dialer is
// DialContext below, and it deliberately has no proxy: an environment proxy
// (HTTP_PROXY) would be a second, unchecked way out.
func (p *Policy) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           p.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       45 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// Client is a client whose transport is policy-checked and whose redirects are
// re-validated hop by hop. It is what the provider probe uses.
func (p *Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: p.Transport(),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return refuse(req.URL.Hostname(), ReasonScheme,
					"refused to follow more than %d redirects from %q", len(via), via[0].URL.Host)
			}
			if _, err := p.Check(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

// DialContext resolves, verifies and then dials the verified address. This is
// the DNS-rebinding half of the policy: the connection goes to the address that
// was checked, and a name whose answer changed between the check and the dial
// fails instead of connecting.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("urlguard: dial %q: %w", addr, err)
	}
	addrs, err := p.verify(ctx, strings.TrimSuffix(strings.ToLower(host), "."), port)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, refuse(host, ReasonDNS, "host %q resolved to no usable address", host)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var last error
	for _, ip := range addrs {
		conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
	}
	return nil, last
}

// ---------------------------------------------------------------------------
// Configuration file
// ---------------------------------------------------------------------------

// fileFormat is the on-disk shape. It mirrors the tenant policies: one JSON
// document, reviewed with the compose file, validated at startup, and strict
// about unknown fields so a typo fails the boot instead of silently leaving the
// default.
type fileFormat struct {
	Comment string  `json:"comment"`
	Allow   []Allow `json:"allow"`
}

// Parse reads an inline policy document, so the format can be tested without a
// filesystem.
func Parse(raw string) (*Policy, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file fileFormat
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("url policy: parse: %w", err)
	}
	policy := Default()
	if err := policy.setAllow(file.Allow, true); err != nil {
		return nil, fmt.Errorf("url policy: %w", err)
	}
	return policy, nil
}

// LoadFile reads the allow-list file. An empty path means "no exceptions",
// which is the default every deployment gets; a path that does not exist is an
// error, because a policy file that silently did not load is a policy the
// operator believes is in force.
func LoadFile(path string) (*Policy, error) {
	if strings.TrimSpace(path) == "" {
		return Default(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("url policy: read %s: %w", path, err)
	}
	policy, err := Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%w (in %s)", err, path)
	}
	return policy, nil
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func ownHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func defaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// redact keeps a URL's host but drops userinfo and query, so a refusal can be
// quoted back to the caller without echoing a token that was pasted into it.
func redact(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<unparseable URL>"
	}
	parsed.User = nil
	if parsed.RawQuery != "" {
		parsed.RawQuery = "redacted"
	}
	return parsed.String()
}
