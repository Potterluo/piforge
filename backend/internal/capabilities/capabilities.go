// Package capabilities answers what this process is allowed to do.
//
// FR-14.2 has two mechanisms for projecting a skill into a worktree. A read-only
// bind mount is the better one: the kernel refuses writes, and many sessions can
// share one snapshot. It needs CAP_SYS_ADMIN, which is a broad capability for a
// control plane that only ever writes files it owns — so the default deployment
// does not ask for it. The read-only copy (copy-ro) is the default story, and a
// read-only copy needs no capability.
//
// The one capability that can make that copy a lie is CAP_DAC_OVERRIDE: a root
// process that holds it ignores the 0444 that marks a copied skill read-only.
// It cannot be dropped from inside the process (Linux only lets a thread change
// its own capability set, and Go runs goroutines on many threads), so the
// deployment drops it with `--cap-drop=DAC_OVERRIDE` / compose `cap_drop`. This
// package is how the control plane checks that this actually happened: a
// deployment that forgot still works, but reports `copy` instead of pretending.
package capabilities

// HasSysAdmin reports whether CAP_SYS_ADMIN is in this process's effective set.
func HasSysAdmin() bool { return hasSysAdmin() }

// DACOverrideEffective reports whether this process can ignore file permission
// bits through CAP_DAC_OVERRIDE.
func DACOverrideEffective() bool { return dacOverrideEffective() }
