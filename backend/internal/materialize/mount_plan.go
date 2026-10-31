package materialize

import "runtime"

// Mechanism is how a skill directory was put into the worktree (FR-14.2).
//
// The honest answer differs per platform, so it is named and recorded instead of
// being called "a mount" everywhere:
//
//   - bind-ro — Linux with a kernel that allows bind mounts (CAP_SYS_ADMIN in
//     the container, or a host process). The kernel enforces read-only, and many
//     sessions can share one snapshot without copying it.
//   - copy-ro — Windows (no bind mount), and a Linux process that is not root.
//     Each skill is copied once at provision time and the files are marked
//     read-only, which the platform honours for this account. The copy is per
//     worktree, so a large skill set is duplicated; that is the cost of not
//     having a mount.
//   - copy — the fallback when even read-only bits are advisory: a root process
//     on Linux without CAP_SYS_ADMIN. Root overrides permission bits
//     (CAP_DAC_OVERRIDE), so a 0444 file is still writable by the agent. The
//     tree is frozen (a running session keeps it) but it is *not* read-only, and
//     the mechanism says so instead of claiming copy-ro.
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
	// in the directory is still possible. On Unix a non-root process gets
	// 0444/0555, which also refuses new files.
	MechanismCopyRO Mechanism = "copy-ro"
	// MechanismCopy is a frozen copy whose read-only bits this process can
	// bypass (root on Linux without CAP_SYS_ADMIN). Honest naming matters more
	// than a nicer table: the tree is a copy, not a mount, and not read-only.
	MechanismCopy Mechanism = "copy"
)

// PlatformMechanism is the mechanism a platform gets before any runtime probe.
// Linux prefers a bind mount and falls back to copy-ro/copy if mount(2) is
// refused; every other platform is copy-ro from the start.
func PlatformMechanism(goos string) Mechanism {
	if goos == "linux" {
		return MechanismBindRO
	}
	return MechanismCopyRO
}

// platformMechanism is PlatformMechanism for the running binary, with a test
// override so the decision is testable on one machine.
func platformMechanism(override string) Mechanism {
	if override != "" {
		return PlatformMechanism(override)
	}
	return PlatformMechanism(runtime.GOOS)
}
