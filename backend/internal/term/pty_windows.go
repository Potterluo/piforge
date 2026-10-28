//go:build windows

package term

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// shellCandidates lists the shells to try, best first.
func shellCandidates(preferred string) []string {
	out := make([]string, 0, 3)
	if strings.TrimSpace(preferred) != "" {
		out = append(out, preferred)
	}
	return append(out, "powershell.exe", "cmd.exe")
}

// pipeProc is the degraded Windows implementation: stdin/stdout pipes instead
// of a ConPTY. Line-oriented shells (echo, dir, git, npm) work; full-screen
// TUIs and window resizing do not.
type pipeProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *io.PipeReader
	outW  *io.PipeWriter

	mu     sync.Mutex
	cols   int
	rows   int
	closed bool
	once   sync.Once
}

// startProcess starts shellPath with piped stdio.
//
// ctx is intentionally not bound to the process: the shell outlives the HTTP
// request that opened it.
// shellArgv builds the argv for an interactive shell. Windows needs flags: a
// bare powershell.exe opens a window and exits.
func shellArgv(shellPath string) []string {
	base := strings.ToLower(filepath.Base(shellPath))
	switch {
	case strings.Contains(base, "powershell"), strings.Contains(base, "pwsh"):
		// "-NoExit" keeps the host interactive over pipes: it prints the prompt
		// and echoes what is typed, which is the difference between a terminal
		// that looks dead and one that works. chcp + OutputEncoding make both
		// directions UTF-8, because the browser decodes UTF-8 and the console
		// otherwise speaks an OEM code page.
		return []string{
			shellPath, "-NoLogo", "-NoProfile", "-NoExit", "-Command",
			"chcp 65001 > $null; " +
				"[Console]::OutputEncoding = [Console]::InputEncoding = [Text.UTF8Encoding]::new()",
		}
	case strings.Contains(base, "cmd.exe"), base == "cmd":
		return []string{shellPath, "/Q", "/K", "chcp 65001>nul"}
	default:
		return []string{shellPath}
	}
}
func startProcess(ctx context.Context, argv []string, dir string, cols, rows int) (ptyProcess, error) {
	_ = ctx
	if len(argv) == 0 {
		return nil, errors.New("term: empty argv")
	}

	pr, pw := io.Pipe()
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "TERM=dumb", "PIFORGE_TERM=1")
	cmd.Stdout = pw
	cmd.Stderr = pw

	stdin, err := cmd.StdinPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		stdin.Close()
		return nil, err
	}
	return &pipeProc{cmd: cmd, stdin: stdin, out: pr, outW: pw, cols: cols, rows: rows}, nil
}

// Read reads merged stdout+stderr.
func (p *pipeProc) Read(b []byte) (int, error) { return p.out.Read(b) }

// Write writes to the shell's stdin.
func (p *pipeProc) Write(b []byte) (int, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	return p.stdin.Write(b)
}

// Resize only records the size: pipes have no window size.
func (p *pipeProc) Resize(cols, rows int) error {
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	return nil
}

// PID reports the shell pid.
func (p *pipeProc) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Wait blocks until the shell exits and closes the output side so the reader
// sees EOF.
func (p *pipeProc) Wait() error {
	err := p.cmd.Wait()
	_ = p.outW.Close()
	return err
}

// Close terminates the shell and unblocks any pending Read. Safe to call
// repeatedly.
func (p *pipeProc) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.outW.Close()
		_ = p.out.Close()
	})
	return nil
}
