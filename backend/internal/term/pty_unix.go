//go:build !windows

package term

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// shellCandidates lists the shells to try, best first.
func shellCandidates(preferred string) []string {
	out := make([]string, 0, 3)
	if strings.TrimSpace(preferred) != "" {
		out = append(out, preferred)
	}
	return append(out, "bash", "sh")
}

// unixPTY is a shell attached to a real pseudo terminal.
type unixPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
}

// startProcess starts shellPath under a PTY of the given size.
//
// ctx is intentionally not bound to the process: an HTTP request context is
// cancelled right after Open returns and must not kill the user's shell.
// shellArgv builds the argv for an interactive shell. Interactive on purpose:
// without it bash exits immediately and the terminal looks broken.
func shellArgv(shellPath string) []string {
	return []string{shellPath, "-i"}
}

func startProcess(ctx context.Context, argv []string, dir string, cols, rows int) (ptyProcess, error) {
	_ = ctx
	if len(argv) == 0 {
		return nil, errors.New("term: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = terminalEnv()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

// Read reads PTY output.
func (p *unixPTY) Read(b []byte) (int, error) { return p.f.Read(b) }

// Write writes to the PTY master.
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

// Resize updates the terminal window size (TIOCSWINSZ).
func (p *unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// PID reports the shell pid.
func (p *unixPTY) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Wait blocks until the shell exits.
func (p *unixPTY) Wait() error { return p.cmd.Wait() }

// Close closes the master and terminates the shell. Safe to call repeatedly.
func (p *unixPTY) Close() error {
	var err error
	p.once.Do(func() {
		if p.f != nil {
			err = p.f.Close()
		}
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGHUP)
			_ = p.cmd.Process.Kill()
		}
	})
	return err
}
