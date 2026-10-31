package materialize

import (
	"runtime"

	"github.com/piforge/piforge/backend/internal/capabilities"
)

// Mechanism is how a skill directory was put into the worktree (FR-14.2).
//
// The honest answer differs per platform, so it is named and recorded instead of
// being called "a mount" everywhere:
//
//   - bind-ro — Linux with CAP_SYS_ADMIN in its effective set (a host process, or
//     a container started with --cap-add SYS_ADMIN). The kernel enforces
//     read-only, and many sessions can share one snapshot without copying it.
//   - copy-ro — the default story, and it needs no capability: every platform
//     except Linux makes such a copy, and so does Linux without CAP_SYS_ADMIN.
//     Each skill is copied once at provision time and the files are marked
//     read-only, which refuses overwriting and deleting them. Windows maps the
//     bit to FILE_ATTRIBUTE_READONLY (the same account can still create a new
//     file in the directory); Unix uses 0444/0555, and the control plane gives up
//     CAP_DAC_OVERRIDE at startup so that root cannot ignore them either. The
//     copy is per worktree, so a large skill set is duplicated; that is the cost
//     of not having a mount.
//   - copy — the fallback when even read-only bits are advisory for this process:
//     a root process that still carries CAP_DAC_OVERRIDE (a hand-started daemon
//     outside the shipped image, or a Linux host where dropping failed). The tree
//     is frozen (a running session keeps it) but it is *not* read-only, and the
//     mechanism says so instead of claiming copy-ro.
//
// Neither copy mechanism points at the live bundle: a running session must not
// see an edit made after it started (the FR-14.5 record is the fact). A new
// session gets a fresh copy or snapshot, which is how edits reach it.
type Mechanism string

const (
	// MechanismBindRO is a read-only bind mount of a frozen snapshot.
	MechanismBindRO Mechanism = "bind-ro"
	// MechanismCopyRO is a per-skill copy with read-only files, enforced by the
	// platform for this account. On Windows this means FILE_ATTRIBUTE_READONLY:
	// overwriting or deleting a mounted file is refused, but creating a new file
	// in the directory is still possible. On Unix a non-root process — and root
	// after the startup capability drop — gets 0444/0555, which also refuses new
	// files.
	MechanismCopyRO Mechanism = "copy-ro"
	// MechanismCopy is a frozen copy whose read-only bits this process can
	// bypass (root that still carries CAP_DAC_OVERRIDE). Honest naming matters
	// more than a nicer table: the tree is a copy, not a mount, and not
	// read-only.
	MechanismCopy Mechanism = "copy"
)

// Capabilities is what the running process may do. It is a value, not a global,
// so the decision order is testable on one machine.
type Capabilities struct {
	// BindRO is true when this process may create a read-only bind mount, i.e.
	// CAP_SYS_ADMIN is in its effective set.
	BindRO bool
}

// ProbeCapabilities reports what the running process can do. It reads the
// kernel's own capability set rather than attempting a probe mount: a mount
// would need a target, would race provisioning, and would have to interpret
// errno.
func ProbeCapabilities() Capabilities {
	return Capabilities{BindRO: capabilities.HasSysAdmin()}
}

// Plan is the mechanism decision for one mount pass, made before any per-skill
// work.
type Plan struct {
	// Preferred is what the pass will try first: bind-ro when the capability is
	// there, otherwise copy-ro.
	Preferred Mechanism
	// CanBind is whether a bind mount may be attempted at all.
	CanBind bool
}

// PlanFor decides the mechanism in the order FR-14.2 needs:
//
//  1. a platform without bind mounts (Windows, macOS) → copy-ro;
//  2. Linux with CAP_SYS_ADMIN → bind-ro;
//  3. Linux without it → copy-ro, never copy. A read-only copy needs no
//     capability, and a deployment must not claim a mount it cannot make.
//
// copy is not a plan. It is the outcome of the read-only probe after the copy
// (see installSkill): the only case where the platform cannot even preserve a
// read-only file for this process, which is reported rather than papered over.
func PlanFor(goos string, caps Capabilities) Plan {
	if goos != "linux" {
		return Plan{Preferred: MechanismCopyRO}
	}
	if caps.BindRO {
		return Plan{Preferred: MechanismBindRO, CanBind: true}
	}
	return Plan{Preferred: MechanismCopyRO}
}

// PlatformPlan is PlanFor with an optional platform override, so a test of the
// decision does not depend on the machine it runs on.
func PlatformPlan(platformOverride string, caps Capabilities) Plan {
	if platformOverride != "" {
		return PlanFor(platformOverride, caps)
	}
	return PlanFor(runtime.GOOS, caps)
}
