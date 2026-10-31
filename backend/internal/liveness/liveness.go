// Package liveness answers the one question restart reconciliation is allowed
// to ask before it writes "interrupted": is an agent still serving this session?
//
// # Why a probe and not just the registry
//
// internal/reconcile used to ask pi.Runtime.Alive, which is an *in-process*
// registry. That is the whole truth for -sandbox local: the agent is a child of
// the control plane, so a restart kills it and every persisted "running" row
// really is an orphan.
//
// With -sandbox docker or -sandbox runner the agent lives beside its workspace,
// outside this process, and survives a control-plane restart. After such a
// restart the registry is empty by definition — the new process has spawned
// nothing — so the registry says "dead" about runs that are still executing. A
// probe is the seam where a provider can answer from where the agent actually
// lives.
//
// # The three answers
//
// Every probe here returns (bool, error) and the pair is the contract
// (contracts.LivenessProbe):
//
//   - (true, nil) — an agent serves the session; reconciliation leaves it alone.
//   - (false, nil) — nobody serves it; reconciliation may interrupt it.
//   - (_, err) — the authority could not answer. Reconciliation treats this as
//     "unknown" and leaves the session running. An unreachable sandbox is not
//     evidence that a run is dead.
//
// # What backs each provider
//
//   - local: Process, wrapping the in-process registry (pi.Runtime).
//   - docker, runner: a heartbeat marker file that the agent's own launcher
//     keeps fresh inside the sandbox workspace (MarkerDir), read by AIO over the
//     sandbox's shell API. A marker rather than `pgrep -f <session>`: a process
//     listing cannot tell an agent from a `grep` of the marker path, matches a
//     just-killed-but-unreaped process, and needs the sandbox to expose a
//     process table. A file the launcher owns and removes on exit is a lease
//     with an explicit writer, and its age is evidence in its own right (a
//     marker that stopped being touched means the writer is gone).
//   - Composite: ask every configured source; alive if any proves alive, unknown
//     if none does and at least one could not answer.
package liveness

import (
	"context"
	"errors"
	"fmt"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Registry is the in-process authority: the set of agent processes this control
// plane itself started. *pi.Runtime implements it; keeping the dependency as
// this one question is why this package does not import internal/pi.
type Registry interface {
	Alive(sessionID string) bool
}

// Process is the local provider's liveness source: the in-process registry.
type Process struct {
	registry Registry
}

var _ contracts.LivenessProbe = (*Process)(nil)

// NewProcess builds the registry-backed probe.
func NewProcess(registry Registry) *Process { return &Process{registry: registry} }

// Alive reports whether this process is serving sessionID.
//
// A nil registry is an error, not "dead": a deployment that forgot to wire the
// authority must not have its runs interrupted on the strength of an empty
// struct.
func (p *Process) Alive(ctx context.Context, sessionID string) (bool, error) {
	if p == nil || p.registry == nil {
		return false, errors.New("liveness: no in-process agent registry is configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return p.registry.Alive(sessionID), nil
}

// Composite asks several probes and reconciles their answers.
//
// The rule is deliberately monotone in "alive": a single positive proof wins,
// because proving an agent exists is enough to forbid interrupting it. Only when
// no source proves alive does the composite look at failures — any failure there
// means "unknown", and unknown is never turned into "dead" (see the package
// comment). A composite with no sources fails closed for the same reason.
type Composite struct {
	sources []contracts.LivenessProbe
}

var _ contracts.LivenessProbe = (*Composite)(nil)

// NewComposite builds a composite, dropping nil sources so a caller can pass an
// unconfigured probe without a branch.
func NewComposite(sources ...contracts.LivenessProbe) *Composite {
	kept := make([]contracts.LivenessProbe, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			kept = append(kept, source)
		}
	}
	return &Composite{sources: kept}
}

// Alive implements contracts.LivenessProbe.
func (c *Composite) Alive(ctx context.Context, sessionID string) (bool, error) {
	if c == nil || len(c.sources) == 0 {
		return false, errors.New("liveness: no liveness sources are configured")
	}
	var errs []error
	for _, source := range c.sources {
		alive, err := source.Alive(ctx, sessionID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if alive {
			return true, nil
		}
	}
	if len(errs) > 0 {
		return false, errors.Join(errs...)
	}
	return false, nil
}

// Sources reports how many probes this composite asks. It is for the one startup
// log line, so an operator can see which authorities a deployment trusts.
func (c *Composite) Sources() int {
	if c == nil {
		return 0
	}
	return len(c.sources)
}

// ProbeFunc adapts a function to contracts.LivenessProbe. Tests use it; nothing
// in production does.
type ProbeFunc func(ctx context.Context, sessionID string) (bool, error)

// Alive implements contracts.LivenessProbe.
func (f ProbeFunc) Alive(ctx context.Context, sessionID string) (bool, error) {
	if f == nil {
		return false, errors.New("liveness: probe function is nil")
	}
	return f(ctx, sessionID)
}

// Describe is the label a startup log prints for a probe, so "which authority
// answered" is on the record rather than inferred from a type name.
func Describe(probe contracts.LivenessProbe) string {
	switch p := probe.(type) {
	case nil:
		return "none"
	case *Process:
		return "process-registry"
	case *Sandbox:
		if p.cfg.Reader == nil {
			return "sandbox-marker(unconfigured)"
		}
		return "sandbox-marker(" + p.cfg.Reader.Describe() + ")"
	case *Composite:
		parts := make([]string, 0, p.Sources())
		for _, source := range p.sources {
			parts = append(parts, Describe(source))
		}
		return fmt.Sprintf("composite[%s]", join(parts, " "))
	default:
		return fmt.Sprintf("%T", probe)
	}
}

func join(parts []string, sep string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += sep
		}
		out += part
	}
	return out
}
