package term

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// terminalOverrides are the environment entries every PTY shell starts with.
//
// A terminal is an interaction surface, not a build log. A pager that waits for
// `q` makes `git log` look like a hung shell and turns the next keystrokes into
// `less` commands — the acceptance pass saw exactly that, complete with `(END)`
// and no hint. `GIT_PAGER=cat` and `PAGER=cat` make git and anything else that
// respects PAGER print and return the prompt. The other two: a credential prompt
// waiting on a socket nobody is reading hangs the tab, and a modern TERM is what
// makes colour and `clear` behave.
var terminalOverrides = []string{
	"TERM=xterm-256color",
	"PIFORGE_TERM=1",
	"PAGER=cat",
	"GIT_PAGER=cat",
	"GIT_TERMINAL_PROMPT=0",
}

// terminalEnv returns the process environment with terminalOverrides applied.
//
// Existing values are replaced rather than shadowed. execve passes duplicates
// through and libc's getenv returns the first match, so appending `PAGER=cat`
// after an inherited `PAGER=less` would leave the child reading `less`.
func terminalEnv() []string {
	replace := make(map[string]bool, len(terminalOverrides))
	for _, kv := range terminalOverrides {
		if key, _, ok := strings.Cut(kv, "="); ok {
			replace[key] = true
		}
	}
	env := make([]string, 0, len(os.Environ())+len(terminalOverrides))
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if ok && replace[key] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, terminalOverrides...)
}

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
