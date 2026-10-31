// Package tenantegress is the per-tenant outbound-network policy.
//
// The tenancy batch closed the last *inward* hole (a tenant reaching another
// tenant) and left the outward one open: a sandbox on its tenant bridge could
// still reach the public internet, so `npm install`, a `curl` of an
// exfiltration endpoint, or a compromised dependency had exactly the reach the
// host has. Nothing in the deployment said whether that was intended.
//
// This package holds the policy that answers the question; internal/sandbox is
// where it becomes a Docker fact (the tenant bridge's `--internal` flag). The
// split is the same one internal/tenantlimits has with the container limits:
// the deployment's configuration is resolved here, and the process that creates
// the container applies it.
//
// Three shapes are modelled, and the difference between them is not cosmetic:
//
//	none       no egress beyond the containers on the tenant's own bridge — the
//	           control plane (so /gw/llm and /gw/mcp keep working) and the
//	           runner. Enforced with `docker network create --internal`.
//	allowlist  the tenant may reach these hosts/CIDRs/ports. Modelled so a
//	           policy can be written and validated, but *refused* at startup:
//	           Docker filters a bridge, not a hostname, and a half-enforced
//	           allowlist is the failure this package exists to prevent. See
//	           sandbox.EgressMode.Enforceable.
//	open       today's behaviour, now an explicit choice: the bridge is ordinary
//	           and Docker NATs it.
//
// Why a file and not a table: the same reason the quota file has one (§12.2). It
// is reviewed with the compose file, versioned with the repository, and validated
// at startup, so a typo fails the boot instead of becoming a tenant that is open
// when the page says closed. Changing a policy is a restart, and that is the cost.
//
// Why a second file rather than a section of deploy/tenant-limits.json: the two
// policies are read by different processes with different parsers. The quota file
// is read only by the control plane, which sends the resolved numbers with each
// create; the egress file must be read by the *runner* too, because the runner is
// the process that creates the bridge. Putting both in one document would mean
// both processes parse both schemas (both reject unknown fields, deliberately),
// and a quota typo would be able to fail the runner's boot for no reason. Two
// files, mounted side by side, validated separately, reviewed together.
package tenantegress

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// Allow is one destination of an allowlist. Exactly one of Host and CIDR is set;
// Ports is required, because "any port on registry.npmjs.org" is a much larger
// grant than it looks.
type Allow struct {
	// Host is a DNS name, e.g. "registry.npmjs.org". No scheme, no path, no
	// wildcard: a wildcard cannot be enforced by any of the mechanisms this
	// platform offers, so accepting one would be a promise the product keeps only
	// on paper.
	Host string `json:"host,omitempty"`
	// CIDR is a network, e.g. "203.0.113.0/24".
	CIDR string `json:"cidr,omitempty"`
	// Ports are the TCP ports this destination may be reached on.
	Ports []int `json:"ports,omitempty"`
}

// Egress is one tenant's outbound policy.
type Egress struct {
	Mode sandbox.EgressMode `json:"mode"`
	// Allow is only meaningful for Mode == allowlist. It is never inherited from
	// the default: an override that names a mode and no destinations gets no
	// destinations, because inheriting another tenant's grant would be a silent
	// widening nobody wrote.
	Allow []Allow `json:"allow,omitempty"`
}

// Validate rejects a policy entry the product cannot mean.
func (e Egress) Validate() error {
	mode, err := sandbox.ParseEgressMode(string(e.Mode))
	if err != nil {
		return err
	}
	if mode != sandbox.EgressAllowlist {
		if len(e.Allow) > 0 {
			return fmt.Errorf(`mode %q has %d allow entries; "allow" only applies to mode "allowlist" `+
				`and would be ignored`, string(mode), len(e.Allow))
		}
		return nil
	}
	if len(e.Allow) == 0 {
		return fmt.Errorf(`mode "allowlist" needs at least one "allow" entry; an empty allowlist would ` +
			`mean "reach nothing", which is what mode "none" already says`)
	}
	for i, entry := range e.Allow {
		if err := entry.validate(); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
	}
	return nil
}

func (a Allow) validate() error {
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
			return fmt.Errorf(`host %q: wildcards cannot be enforced on this platform, name the hosts`, host)
		}
		if !strings.Contains(host, ".") && host != "localhost" {
			return fmt.Errorf(`host %q is not a domain name`, host)
		}
	}
	if cidr != "" {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("cidr %q: %w", cidr, err)
		}
	}
	if len(a.Ports) == 0 {
		return fmt.Errorf("no ports; name the TCP ports this destination may be reached on (e.g. [443])")
	}
	for _, port := range a.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("port %d is outside 1-65535", port)
		}
	}
	return nil
}

// Policy is a default plus per-tenant overrides.
type Policy struct {
	def     Egress
	tenants map[string]Egress
	// source names where this policy came from, for the boot log and the admin
	// screen: a file path, "flags", or "network-mode".
	source string
}

// New builds a policy that gives every tenant the same mode. source names where
// it came from ("flags", "network-mode"), so the admin screen can point at the
// line that decided it.
func New(def Egress, source string) *Policy {
	if strings.TrimSpace(source) == "" {
		source = "flags"
	}
	return &Policy{def: def, tenants: map[string]Egress{}, source: source}
}

// Source names where the policy was read from.
func (p *Policy) Source() string {
	if p == nil {
		return ""
	}
	return p.source
}

// Default is what a tenant without an override gets.
func (p *Policy) Default() Egress {
	if p == nil {
		return Egress{}
	}
	return p.def
}

// For resolves one tenant's policy. The user id is checked first because it is
// what the caller always has; the email is accepted too because that is what an
// operator writes in the file.
func (p *Policy) For(userID, email string) Egress {
	if p == nil {
		return Egress{}
	}
	if entry, ok := p.lookup(userID); ok {
		return entry
	}
	if entry, ok := p.lookup(email); ok {
		return entry
	}
	return p.def
}

func (p *Policy) lookup(key string) (Egress, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return Egress{}, false
	}
	entry, ok := p.tenants[key]
	return entry, ok
}

// Tenants lists the keys the policy names, sorted, so the admin screen can show
// an override for an account that has no sandbox yet.
func (p *Policy) Tenants() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.tenants))
	for key := range p.tenants {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Check is the startup question: can this deployment actually apply every mode
// this policy names?
//
// It is deliberately a *refusal* and not a warning. An operator who believes
// egress is closed while it is open is worse off than one whose deployment
// refused to start: the first finds out from an incident, the second from a line
// of stderr.
func (p *Policy) Check(networkMode sandbox.NetworkMode) error {
	if p == nil {
		return nil
	}
	if err := p.def.Validate(); err != nil {
		return fmt.Errorf("default: %w", err)
	}
	for _, key := range p.Tenants() {
		if err := p.tenants[key].Validate(); err != nil {
			return fmt.Errorf("tenant %q: %w", key, err)
		}
	}
	// A mode this platform cannot enforce fails here, naming the tenant.
	if err := p.def.Mode.Enforceable(); err != nil {
		return fmt.Errorf("default: %w", err)
	}
	for _, key := range p.Tenants() {
		if err := p.tenants[key].Mode.Enforceable(); err != nil {
			return fmt.Errorf("tenant %q: %w", key, err)
		}
	}
	if networkMode != sandbox.NetworkTenant {
		// A shared bridge is the single-host shape: no per-tenant anything.
		if len(p.tenants) > 0 {
			return fmt.Errorf("the policy has %d per-tenant override(s) but sandboxes share the "+
				`network (-sandbox-network-mode shared): a per-tenant egress policy needs a bridge per `+
				`tenant, and ignoring the overrides is not an option`, len(p.tenants))
		}
		if p.def.Mode != sandbox.EgressOpen {
			return fmt.Errorf("egress mode %q cannot be enforced on a shared network: Docker cannot "+
				"make one tenant's traffic closed while another's is open on the same bridge; use "+
				"-sandbox-network-mode tenant, or say -tenant-egress-mode open (and mean it)",
				string(p.def.Mode))
		}
	}
	return nil
}

// CheckHost is Check for a deployment with no container at all (-sandbox local).
//
// There is no bridge to close there: the agent is a process on the host and
// shares the host's network namespace, so "none" is not enforceable and "open"
// is the only true statement. A policy that asks for more is refused rather than
// accepted and quietly not applied.
func (p *Policy) CheckHost() error {
	if p == nil {
		return nil
	}
	if err := p.def.Validate(); err != nil {
		return fmt.Errorf("default: %w", err)
	}
	if p.def.Mode != sandbox.EgressOpen {
		return fmt.Errorf("egress mode %q cannot be enforced with -sandbox local: the agent runs on "+
			`the host and shares its network namespace, so there is no bridge to close; use a `+
			`container placement (-sandbox runner or docker) or say "open"`,
			string(p.def.Mode))
	}
	for _, key := range p.Tenants() {
		if err := p.tenants[key].Validate(); err != nil {
			return fmt.Errorf("tenant %q: %w", key, err)
		}
		if p.tenants[key].Mode != sandbox.EgressOpen {
			return fmt.Errorf("tenant %q: egress mode %q cannot be enforced with -sandbox local "+
				"(the agent runs on the host; there is no bridge to close)", key, string(p.tenants[key].Mode))
		}
	}
	return nil
}

// wireEgress is the JSON shape with a pointer, so "absent" and "explicitly
// none" are different requests: an entry that names only a mode keeps the
// default's other fields out of the picture (allow is never inherited).
type wireEgress struct {
	Mode  *string `json:"mode"`
	Allow []Allow `json:"allow"`
}

type wirePolicy struct {
	Default *wireEgress           `json:"default"`
	Tenants map[string]wireEgress `json:"tenants"`
	Comment string                `json:"comment"`
}

func (w wireEgress) resolve(def Egress) Egress {
	out := Egress{Mode: def.Mode}
	if w.Mode != nil {
		// Normalise here so "NONE", " none " and "" all resolve to the canonical
		// value the provider compares against: an empty mode is none, never open.
		raw := strings.ToLower(strings.TrimSpace(*w.Mode))
		if parsed, err := sandbox.ParseEgressMode(raw); err == nil {
			out.Mode = parsed
		} else {
			// Kept as written so Validate reports the operator's string.
			out.Mode = sandbox.EgressMode(raw)
		}
	}
	// Never inherited: see Egress.Allow.
	out.Allow = append([]Allow(nil), w.Allow...)
	return out
}

// LoadFile reads a policy file and merges it over the deployment fallback.
func LoadFile(path string, def Egress) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tenant egress: read %s: %w", path, err)
	}
	return parse(string(raw), def, path)
}

// Parse parses an inline policy document, so the format can be tested without
// touching the filesystem.
func Parse(raw string, def Egress) (*Policy, error) { return parse(raw, def, "inline") }

func parse(raw string, def Egress, source string) (*Policy, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	// An operator's typo ("mdoe") must fail the boot: silently ignoring it would
	// leave the deployment at the default while the file reads as if it said
	// something else.
	decoder.DisallowUnknownFields()
	var wire wirePolicy
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("tenant egress: parse %s: %w", source, err)
	}
	policy := &Policy{def: def, tenants: make(map[string]Egress, len(wire.Tenants)), source: source}
	if wire.Default != nil {
		policy.def = wire.Default.resolve(def)
	}
	if err := policy.def.Validate(); err != nil {
		return nil, fmt.Errorf("tenant egress: default in %s: %w", source, err)
	}
	for key, entry := range wire.Tenants {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("tenant egress: %s has an empty tenant key", source)
		}
		resolved := entry.resolve(policy.def)
		if err := resolved.Validate(); err != nil {
			return nil, fmt.Errorf("tenant egress: tenant %q in %s: %w", key, source, err)
		}
		policy.tenants[key] = resolved
	}
	return policy, nil
}

// Describes renders a policy for a log line or the admin screen: the mode plus
// a count of destinations, never the destinations themselves (a host list is an
// operator's business, not a symptom).
func (e Egress) Describes() string {
	if e.Mode != sandbox.EgressAllowlist || len(e.Allow) == 0 {
		return string(e.Mode)
	}
	return string(e.Mode) + "(" + strconv.Itoa(len(e.Allow)) + " entries)"
}
