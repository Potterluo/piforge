//go:build linux

package agentsupervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// childProcAttr makes the agent die with its supervisor.
//
// The supervisor is the agent's owner now. If it is killed (the container is
// stopped, the OOM reaper picks it, the sandbox is destroyed), leaving `pi`
// running would leave a process that keeps writing its heartbeat? No — the
// launcher is the parent of pi and it would die too, so the marker would be
// removed; but pi itself would keep burning CPU in a sandbox nobody can reach.
// Linux propagates the signal; other platforms rely on stdin EOF, which the
// supervisor's own death also closes.
func childProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// detachedProcAttr puts the supervisor in its own session, with no controlling
// terminal and no process group shared with the docker exec that started it.
// This is what makes a control-plane restart unable to signal it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminationSignals are what `docker stop` sends the container's processes.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func execCommand(name string, args []string) *exec.Cmd { return exec.Command(name, args...) }
