//go:build !linux

package agentsupervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// childProcAttr is nil off Linux: no portable parent-death signal exists, and
// the supervisor closes the agent's stdin when it exits, which Pi treats as the
// end of its session anyway.
func childProcAttr() *syscall.SysProcAttr { return nil }

// detachedProcAttr is nil off Linux. The supervisor is a child process of the
// helper that started it; this platform has no setsid, and no deployment runs a
// sandbox supervisor on it (the sandbox is a Linux container).
func detachedProcAttr() *syscall.SysProcAttr { return nil }

// terminationSignals is just the interrupt: syscall.SIGTERM is not a catchable
// signal on Windows.
func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func execCommand(name string, args []string) *exec.Cmd { return exec.Command(name, args...) }
