package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Limits is what one sandbox is allowed to consume on the host.
//
// It exists because the deployment-wide pair of flags (-sandbox-cpus /
// -sandbox-memory) answers "how big may a sandbox be", not "how big may *this
// tenant's* sandbox be". Multi-tenancy is an operating property only if the
// answer depends on the tenant: one account running a fork bomb must not be able
// to take the host from everyone else, and an operator must be able to give a
// heavy tenant more without giving it to all of them.
//
// The zero value means "no limit", which is the single-user default and a bad
// one for a shared deployment; the deployment decides by configuring a policy.
type Limits struct {
	// CPUs is Docker's --cpus value ("2", "0.5"). Empty means unlimited.
	CPUs string
	// Memory is Docker's --memory value ("2g", "512m"). Empty means unlimited.
	Memory string
	// Pids is Docker's --pids-limit: how many processes and threads the container
	// may have. Zero means unlimited, which is the value that lets one sandbox
	// exhaust the host's PID table.
	Pids int
}

// IsZero reports whether this limit set constrains nothing.
func (l Limits) IsZero() bool {
	return strings.TrimSpace(l.CPUs) == "" && strings.TrimSpace(l.Memory) == "" && l.Pids <= 0
}

// String renders the limit the way an operator reads it in a log line.
func (l Limits) String() string {
	parts := make([]string, 0, 3)
	if l.CPUs != "" {
		parts = append(parts, "cpus="+l.CPUs)
	}
	if l.Memory != "" {
		parts = append(parts, "memory="+l.Memory)
	}
	if l.Pids > 0 {
		parts = append(parts, "pids="+strconv.Itoa(l.Pids))
	}
	if len(parts) == 0 {
		return "unlimited"
	}
	return strings.Join(parts, " ")
}

// Validate rejects a limit that Docker would refuse at create time, so a typo in
// a policy file fails the deployment's startup instead of the first session.
func (l Limits) Validate() error {
	if raw := strings.TrimSpace(l.CPUs); raw != "" {
		cpus, err := strconv.ParseFloat(raw, 64)
		if err != nil || cpus <= 0 {
			return fmt.Errorf("cpus %q is not a positive number of CPUs (for example 2 or 0.5)", raw)
		}
	}
	if raw := strings.TrimSpace(l.Memory); raw != "" {
		if _, err := ParseMemMB(raw); err != nil {
			return err
		}
	}
	if l.Pids < 0 {
		return fmt.Errorf("pids %d must not be negative (0 means unlimited)", l.Pids)
	}
	return nil
}

// ParseCPUMilli turns a Docker --cpus value into thousandths of a CPU, which is
// what the sandbox row keeps so an operator can see the limit that was applied
// without reading the deployment's configuration. Zero means "unlimited" or
// "unparsable", both of which are recorded as "no limit on the row".
func ParseCPUMilli(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	cpus, err := strconv.ParseFloat(raw, 64)
	if err != nil || cpus <= 0 {
		return 0
	}
	return int(cpus*1000 + 0.5)
}

// memoryUnits is Docker's suffix table.
var memoryUnits = map[byte]int64{
	'b': 1,
	'k': 1 << 10,
	'm': 1 << 20,
	'g': 1 << 30,
}

// ParseMemMB turns a Docker --memory value into whole mebibytes. Docker accepts
// "512m", "2g", "1073741824" and the same suffixes with a trailing "b"; the row
// keeps mebibytes because that is how an operator sizes a container.
func ParseMemMB(raw string) (int, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0, nil
	}
	unit := byte('b')
	number := raw
	if last := raw[len(raw)-1]; last < '0' || last > '9' {
		if last == 'b' && len(raw) > 1 {
			// "512mb", "2gb": the unit is the letter before the b.
			prev := raw[len(raw)-2]
			if prev < '0' || prev > '9' {
				unit, number = prev, raw[:len(raw)-2]
			} else {
				number = raw[:len(raw)-1]
			}
		} else {
			unit, number = last, raw[:len(raw)-1]
		}
	}
	factor, ok := memoryUnits[unit]
	if !ok {
		return 0, fmt.Errorf("memory %q: %q is not one of b, k, m, g", raw, string(unit))
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("memory %q is not a positive size (for example 2g or 512m)", raw)
	}
	bytes := value * float64(factor)
	mb := int(bytes/float64(1<<20) + 0.5)
	if mb <= 0 {
		// A sub-mebibyte budget is legal for Docker and meaningless to report as
		// zero: rounding to 0 would read as "no limit" in the admin screen.
		mb = 1
	}
	return mb, nil
}

// Apply writes the limits into a container's `docker run` argv. It is a method
// on the provider because a per-tenant limit is resolved per create; the fixed
// deployment defaults are the fallback when no policy is configured.
func (d *Docker) applyLimits(args []string, limits Limits) []string {
	if cpus := strings.TrimSpace(limits.CPUs); cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if memory := strings.TrimSpace(limits.Memory); memory != "" {
		args = append(args, "--memory", memory)
	}
	if limits.Pids > 0 {
		// The fork-bomb guard: without it a single agent can spawn processes
		// until the host's PID table is full, which takes down every tenant.
		args = append(args, "--pids-limit", strconv.Itoa(limits.Pids))
	}
	return args
}

// appliedLimits reads the limits a container was created with, out of its own
// HostConfig. Docker stores them there in its own units (NanoCPUs, bytes), which
// is why this does not just echo the policy.
func (d *Docker) appliedLimits(ctx context.Context, name string) (Limits, error) {
	out, err := d.docker(ctx, "inspect", "--format", "{{json .HostConfig}}", name)
	if err != nil {
		return Limits{}, err
	}
	var host struct {
		NanoCpus  int64  `json:"NanoCpus"`
		Memory    int64  `json:"Memory"`
		PidsLimit *int64 `json:"PidsLimit"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &host); err != nil {
		return Limits{}, fmt.Errorf("sandbox: docker: inspect %s host config: %w", name, err)
	}
	applied := Limits{}
	if host.NanoCpus > 0 {
		applied.CPUs = strconv.FormatFloat(float64(host.NanoCpus)/1e9, 'f', -1, 64)
	}
	if host.Memory > 0 {
		applied.Memory = strconv.FormatInt(host.Memory/(1<<20), 10) + "m"
	}
	if host.PidsLimit != nil && *host.PidsLimit > 0 {
		applied.Pids = int(*host.PidsLimit)
	}
	return applied, nil
}

// RecordedLimits is appliedLimits in the two integers a sandbox row keeps.
func (l Limits) RecordedLimits() (cpuMilli, memMB int) {
	cpuMilli = ParseCPUMilli(l.CPUs)
	memMB, _ = ParseMemMB(l.Memory)
	return cpuMilli, memMB
}

// limitsFor resolves the limits of one tenant's sandbox: the configured policy
// when there is one, the deployment-wide defaults otherwise.
func (d *Docker) limitsFor(ctx context.Context, userID string) Limits {
	if d.cfg.LimitsFor != nil {
		return d.cfg.LimitsFor(ctx, userID)
	}
	return Limits{CPUs: d.cfg.CPUs, Memory: d.cfg.Memory, Pids: d.cfg.Pids}
}
