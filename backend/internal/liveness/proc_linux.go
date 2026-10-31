//go:build linux

package liveness

import (
	"os"
	"syscall"
)

// childProcAttr makes the agent die with its launcher.
//
// Without it, killing the launcher (a control-plane crash, a `docker kill`, the
// bridge's last-resort Process.Kill) would leave Pi running with no heartbeat
// writer: the marker would go stale, the probe would call the session dead, and
// a real run would be interrupted by a bookkeeping mistake — the exact failure
// this package exists to prevent. Linux propagates the signal; other platforms
// rely on stdin EOF closing the agent down.
func childProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// terminationSignals are what an operator's `docker stop` / Ctrl-C sends. The
// launcher forwards the intent by cancelling the context, which kills the agent.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
