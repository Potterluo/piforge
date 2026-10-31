//go:build !linux

package capabilities

// hasSysAdmin is false off Linux: there is no capability set to consult, and no
// bind mount to make (the Windows and macOS mechanisms are copies).
func hasSysAdmin() bool { return false }

// dacOverrideEffective is false off Linux: the platforms PiForge ships on
// enforce read-only files for the owning account.
func dacOverrideEffective() bool { return false }
