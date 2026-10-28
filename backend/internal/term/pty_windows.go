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
		// A tiny REPL instead of "-Command -".
		//
		// "-Command -" writes its output in the console's OEM code page, which
		// the browser then decodes as UTF-8: every non-ASCII character in the
		// terminal became a replacement glyph. Setting OutputEncoding before
		// reading stdin fixes that, and printing exceptions keeps a failed
		// command from looking like a dead shell.
		return []string{
			shellPath, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
			`$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new(); ` +
				// Read stdin through a UTF-8 StreamReader: [Console]::In decodes
				// with the console code page, which turns UTF-8 input from the
				// browser into garbage before the shell ever sees it.
				`$in = New-Object IO.StreamReader([Console]::OpenStandardInput(), [Text.UTF8Encoding]::new()); ` +
				`while (($line = $in.ReadLine()) -ne $null) { ` +
				`if ($line.Trim().Length -eq 0) { continue }; ` +
				`try { Invoke-Expression $line } catch { Write-Error $_.Exception.Message } }`,
		}
	case strings.Contains(base, "cmd.exe"), base == "cmd":
		// 65001 so that programs writing UTF-8 are not mangled on the way out.
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
