//go:build !linux

package liveness

import (
	"os"
	"syscall"
)

// childProcAttr is nil here: neither Windows nor the BSDs offer Linux's
// parent-death signal. The agent still goes away when the bridge closes its
// stdin (Pi exits on EOF), which is the path every platform relies on today.
func childProcAttr() *syscall.SysProcAttr { return nil }

// terminationSignals is just the interrupt outside Linux: syscall.SIGTERM is not
// portable to Windows as a catchable signal.
func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }
