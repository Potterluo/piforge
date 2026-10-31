// Package tenantlimits is the per-tenant quota policy.
//
// Multi-tenancy is an operating property only when one tenant's resource use is
// bounded by something other than the deployment's total: a single agent that
// compiles in a loop must not take the host from everyone else. The deployment
// used to have exactly two knobs (-sandbox-cpus / -sandbox-memory) applied to
// every sandbox equally, which is a *deployment* budget, not a tenant one.
//
// This package holds the policy that replaces them:
//
//   - a default, taken from the deployment-wide flags, which is what a tenant
//     with no entry gets;
//   - optional per-tenant overrides, keyed by user id or by email, in a small
//     JSON file named by PIFORGE_TENANT_LIMITS_FILE.
//
// Why a file and not a database table: the quota is part of the deployment's
// configuration, not of its data. An operator who reviews a deployment reads the
// compose file and the limits file together; a `tenant_limits` row is invisible
// in a code review, is not versioned with the deployment, and needs an admin UI
// to change safely. The file is also the only source that can be validated at
// startup, so a typo fails the boot (with the tenant named) instead of the first
// session. Changing a quota is a restart, and that is the honest cost.
//
// The file is read once, at startup. A policy that could change while the
// process runs would make "the limit this sandbox was created with" and "the
// limit the admin page shows" two different numbers, and the sandbox row is what
// records the one that was applied.
package tenantlimits

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// Limits is one tenant's ceiling. Zero values mean "unlimited" for that field.
type Limits struct {
	// CPUs is docker's --cpus value ("2", "0.5").
	CPUs string `json:"cpus,omitempty"`
	// Memory is docker's --memory value ("2g", "512m").
	Memory string `json:"memory,omitempty"`
	// Pids is docker's --pids-limit; zero means unlimited.
	Pids int `json:"pids,omitempty"`
	// MaxSandboxes is how many sandboxes this tenant may have at once. Zero means
	// unlimited. It is enforced where a sandbox is created (internal/provision), and
	// the refusal names the number.
	MaxSandboxes int `json:"max_sandboxes,omitempty"`
}

// Sandbox is the container-limit part of the policy, in the type the sandbox
// provider consumes.
func (l Limits) Sandbox() sandbox.Limits {
	return sandbox.Limits{CPUs: l.CPUs, Memory: l.Memory, Pids: l.Pids}
}

// Validate rejects a limit the daemon would refuse at create time.
func (l Limits) Validate() error {
	if err := l.Sandbox().Validate(); err != nil {
		return err
	}
	if l.MaxSandboxes < 0 {
		return fmt.Errorf("max_sandboxes %d must not be negative (0 means unlimited)", l.MaxSandboxes)
	}
	return nil
}

// Policy is a default plus per-tenant overrides.
type Policy struct {
	def     Limits
	tenants map[string]Limits
	// source names where this policy came from, for the admin screen and the boot
	// log: "flags" or the file path. An operator reading a limit has to be able to
	// find the line that set it.
	source string
}

// New builds a policy that gives every tenant the same limits. It is what a
// deployment without a limits file gets, and it keeps the old flags meaningful.
func New(def Limits) *Policy {
	return &Policy{def: def, tenants: map[string]Limits{}, source: "flags"}
}

// Source names where the policy was read from.
func (p *Policy) Source() string {
	if p == nil {
		return ""
	}
	return p.source
}

// Default is the limit every tenant without an override gets.
func (p *Policy) Default() Limits {
	if p == nil {
		return Limits{}
	}
	return p.def
}

// For resolves one tenant's limits. The user id is checked first because it is
// what the caller always has; the email is accepted as well because that is what
// an operator writes in a policy file.
//
// An entry that sets only some fields inherits the rest from the default, so a
// policy that says "bob gets 4 CPUs" does not silently remove bob's memory and
// process limits.
func (p *Policy) For(userID, email string) Limits {
	if p == nil {
		return Limits{}
	}
	if entry, ok := p.lookup(userID); ok {
		return entry
	}
	if entry, ok := p.lookup(email); ok {
		return entry
	}
	return p.def
}

func (p *Policy) lookup(key string) (Limits, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return Limits{}, false
	}
	entry, ok := p.tenants[key]
	return entry, ok
}

// Tenants lists the keys the policy names, sorted, so an admin screen can show
// the overrides even for an account that has no sandbox yet.
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


// wireLimits is the JSON shape with pointers, so "absent" and "explicitly 0"
// (meaning unlimited) are different requests.
type wireLimits struct {
	CPUs         *string `json:"cpus"`
	Memory       *string `json:"memory"`
	Pids         *int    `json:"pids"`
	MaxSandboxes *int    `json:"max_sandboxes"`
}

type wirePolicy struct {
	Default *wireLimits           `json:"default"`
	Tenants map[string]wireLimits `json:"tenants"`
	Comment string                `json:"comment"`
}

func (w wireLimits) resolve(def Limits) Limits {
	out := def
	if w.CPUs != nil {
		out.CPUs = strings.TrimSpace(*w.CPUs)
	}
	if w.Memory != nil {
		out.Memory = strings.TrimSpace(*w.Memory)
	}
	if w.Pids != nil {
		out.Pids = *w.Pids
	}
	if w.MaxSandboxes != nil {
		out.MaxSandboxes = *w.MaxSandboxes
	}
	return out
}

// LoadFile reads a policy file and merges it over the deployment defaults.
//
// A missing file is the caller's decision, not this function's: it is only asked
// when a path was configured. An unreadable file, invalid JSON, an unknown field
// or a limit the daemon would refuse is an error naming the tenant, because a
// deployment that cannot enforce what it was told to enforce must not start.
func LoadFile(path string, def Limits) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tenant limits: read %s: %w", path, err)
	}
	return parse(string(raw), def, path)
}

// Parse parses an inline policy document. It exists so the format can be tested
// without touching the filesystem.
func Parse(raw string, def Limits) (*Policy, error) { return parse(raw, def, "inline") }

func parse(raw string, def Limits, source string) (*Policy, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	// Unknown fields are an operator's typo, and silently ignoring "memory_limt"
	// would leave the deployment unlimited while reading as if it were not.
	decoder.DisallowUnknownFields()
	var wire wirePolicy
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("tenant limits: parse %s: %w", source, err)
	}
	policy := &Policy{def: def, tenants: make(map[string]Limits, len(wire.Tenants)), source: source}
	if wire.Default != nil {
		policy.def = wire.Default.resolve(def)
	}
	if err := policy.def.Validate(); err != nil {
		return nil, fmt.Errorf("tenant limits: default in %s: %w", source, err)
	}
	for key, entry := range wire.Tenants {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("tenant limits: %s has an empty tenant key", source)
		}
		// Resolve the override against the default here, once: an entry that names
		// one field keeps the default's other fields, and an explicit 0 (unlimited)
		// stays distinguishable from "not mentioned" only until this point.
		resolved := entry.resolve(policy.def)
		if err := resolved.Validate(); err != nil {
			return nil, fmt.Errorf("tenant limits: tenant %q in %s: %w", key, source, err)
		}
		policy.tenants[key] = resolved
	}
	return policy, nil
}
