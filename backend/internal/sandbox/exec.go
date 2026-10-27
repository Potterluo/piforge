package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Exec runs argv inside the sandbox and returns combined stdout+stderr.
//
// argv is executed directly — never through a shell — so a command containing
// quoting or globs is passed through literally, and injection is impossible.
// dir is resolved inside the sandbox root; empty means the sandbox root.
//
// The call honours ctx cancellation: the child process is killed and the output
// collected so far is returned with an error wrapping ctx.Err().
func (l *Local) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: exec: empty argv")
	}
	sb, err := l.Get(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	switch sb.State {
	case contracts.SandboxPaused:
		return "", fmt.Errorf("%w: %s", ErrPaused, sandboxID)
	case contracts.SandboxTerminated:
		return "", fmt.Errorf("%w: %s", ErrGone, sandboxID)
	case contracts.SandboxMissing:
		return "", fmt.Errorf("%w: %s", ErrGone, sandboxID)
	}

	base := sb.RootDir
	if base == "" {
		return "", fmt.Errorf("%w: %s", ErrGone, sandboxID)
	}
	workDir, err := resolveInside(base, dir)
	if err != nil {
		return "", err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok && l.cfg.ExecTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.cfg.ExecTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(sanitisedEnv(), "PIFORGE_SANDBOX_ID="+sb.ID)
	cmd.WaitDelay = 2 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	combined := out.String()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return combined, fmt.Errorf("sandbox: exec %s: %w", argv[0], ctxErr)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return combined, fmt.Errorf("sandbox: exec %s: exit %d: %w", argv[0], ee.ExitCode(), runErr)
		}
		if errors.Is(runErr, exec.ErrNotFound) {
			return combined, fmt.Errorf("sandbox: exec %s: binary not found: %w", argv[0], runErr)
		}
		return combined, fmt.Errorf("sandbox: exec %s: %w", argv[0], runErr)
	}
	return combined, nil
}

// resolveInside joins dir onto base and refuses anything that leaves base.
func resolveInside(base, dir string) (string, error) {
	if strings.ContainsRune(dir, 0) {
		return "", fmt.Errorf("sandbox: exec: dir contains a NUL byte")
	}
	if strings.TrimSpace(dir) == "" {
		return base, nil
	}
	var p string
	if filepath.IsAbs(dir) {
		p = filepath.Clean(dir)
	} else {
		p = filepath.Join(base, filepath.FromSlash(dir))
	}
	if !isInside(base, p) {
		return "", fmt.Errorf("sandbox: exec: dir %q is outside the sandbox", dir)
	}
	return p, nil
}

// sanitisedEnv strips git redirection variables and forces a non-interactive
// environment. Credentials are never injected here: PRD §6.2-A keeps provider
// keys out of the sandbox entirely.
func sanitisedEnv() []string {
	drop := map[string]bool{
		"GIT_DIR":                          true,
		"GIT_WORK_TREE":                    true,
		"GIT_INDEX_FILE":                   true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_CEILING_DIRECTORIES":          true,
	}
	out := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		if drop[strings.ToUpper(kv[:i])] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1", "TERM=dumb")
}
