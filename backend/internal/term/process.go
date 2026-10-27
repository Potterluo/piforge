package term

import (
	"fmt"
	"os/exec"
	"strings"
)

// ptyProcess is the platform handle to one shell. Unix backs it with a real
// PTY; Windows with stdin/stdout pipes (degraded TTY semantics).
type ptyProcess interface {
	// Read blocks until output is available or the shell exits.
	Read(p []byte) (int, error)
	// Write sends input to the shell's stdin.
	Write(p []byte) (int, error)
	// Resize applies a window size (a no-op on Windows).
	Resize(cols, rows int) error
	// PID is the shell process id.
	PID() int
	// Wait blocks until the shell exits.
	Wait() error
	// Close terminates the shell and releases the handle. It is idempotent.
	Close() error
}

// resolveShell picks an executable shell: the preferred value first, then the
// platform candidates. The returned path is absolute.
func resolveShell(preferred string) (string, error) {
	var tried []string
	for _, cand := range shellCandidates(preferred) {
		tried = append(tried, cand)
		if p, err := exec.LookPath(cand); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("term: no usable shell (tried %s)", strings.Join(tried, ", "))
}
