// Package gitx runs git operations for the PiForge workspace layer.
//
// Everything in this package talks to the git CLI through argv slices — never
// through a shell string — so that paths, branch names and commit messages
// containing spaces, quotes or shell metacharacters cannot be reinterpreted by
// a shell.
//
// Path safety: every path argument a caller passes in is resolved to an
// absolute path and must stay inside the repository it belongs to (see
// CleanRel, SecurePath). Commands always run with a scrubbed environment:
// a GIT_DIR or GIT_WORK_TREE inherited from the daemon (or from a git hook that
// started the daemon) must not be able to redirect them at another repository.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

const (
	// DefaultBinary is the git executable used when WithBinary is not given.
	DefaultBinary = "git"
	// DefaultTimeout bounds a single git invocation.
	DefaultTimeout = 30 * time.Second
)

// ErrPathEscape is returned when a caller-supplied path would leave the
// repository (traversal, absolute path, NUL byte, ...).
var ErrPathEscape = errors.New("gitx: path escapes repository root")

// ErrInvalidRequest is the sentinel behind every syntactic refusal from this
// package: a revision, branch name, cursor, diff range or path the caller wrote
// in a shape git cannot use.
//
// It exists so the HTTP layer can tell apart the two failures that used to look
// identical: a request that is wrong (400) and a control plane that is broken
// (500). docs/DATA-FLOW-AUDIT.md G7 measured `POST /git/branch {"name":"-D"}`
// answering 500 with this package's own guard sentence — the guard held, only
// the status was a lie.
var ErrInvalidRequest = errors.New("gitx: the request names something git cannot use")

// InvalidRequestError is one syntactic refusal, carrying the sentence a person
// reads. It matches ErrInvalidRequest through Is, so a caller can ask "was this
// the caller's mistake?" without parsing English.
type InvalidRequestError struct {
	Detail string
	Cause  error
}

func (e *InvalidRequestError) Error() string {
	if e.Cause != nil {
		return "gitx: " + e.Detail + ": " + e.Cause.Error()
	}
	return "gitx: " + e.Detail
}

func (e *InvalidRequestError) Unwrap() error { return e.Cause }

// Is makes every InvalidRequestError match ErrInvalidRequest.
func (e *InvalidRequestError) Is(target error) bool { return target == ErrInvalidRequest }

// refuseRequest builds one typed refusal.
func refuseRequest(format string, args ...any) error {
	return &InvalidRequestError{Detail: fmt.Sprintf(format, args...)}
}

// CommandError is a git invocation that ran but exited non-zero.
type CommandError struct {
	Args   []string
	Code   int
	Stderr string
	Stdout string
}

// Error implements error.
func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return fmt.Sprintf("gitx: git %s: exit %d: %s", strings.Join(e.Args, " "), e.Code, msg)
}

// ExitCode reports the exit code of a CommandError, or -1.
func ExitCode(err error) int {
	var ce *CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return -1
}

// StderrOf returns the captured stderr of a CommandError, or "".
func StderrOf(err error) string {
	var ce *CommandError
	if errors.As(err, &ce) {
		return ce.Stderr
	}
	return ""
}

// Result is the captured output of one git invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExecFunc runs one git invocation somewhere other than this process's own
// filesystem and reports what it produced.
//
// It exists so that git can run *inside* a sandbox without re-implementing the
// porcelain parsing: the command is identical, only the machine executing it
// changes. dir is the host-side directory the caller named; translating it and
// capturing stdout and stderr separately is the executor's job. argv already
// carries the global `-c` flags. A non-zero exit must come back as
// *CommandError so ExitCode/StderrOf keep working.
type ExecFunc func(ctx context.Context, dir string, stdin []byte, argv []string) (Result, error)

// Runner executes git with a configurable binary, timeout and environment.
// It is safe for concurrent use.
type Runner struct {
	bin      string
	timeout  time.Duration
	extraEnv []string
	exec     ExecFunc
}

// Option configures a Runner.
type Option func(*Runner)

// WithBinary overrides the git executable (default DefaultBinary).
func WithBinary(bin string) Option {
	return func(r *Runner) {
		if strings.TrimSpace(bin) != "" {
			r.bin = bin
		}
	}
}

// WithTimeout bounds a single git invocation. Zero or negative disables the
// per-call timeout (the caller's context still applies).
func WithTimeout(d time.Duration) Option {
	return func(r *Runner) { r.timeout = d }
}

// WithEnv appends KEY=VALUE pairs to the environment of every invocation.
func WithEnv(kv ...string) Option {
	return func(r *Runner) { r.extraEnv = append(r.extraEnv, kv...) }
}

// WithExecutor runs git through fn instead of this process. It is how the
// remote workspace keeps the local one's porcelain parsing and gets it against
// the container's working tree.
func WithExecutor(fn ExecFunc) Option {
	return func(r *Runner) {
		if fn != nil {
			r.exec = fn
		}
	}
}

// NewRunner builds a Runner.
func NewRunner(opts ...Option) *Runner {
	r := &Runner{bin: DefaultBinary, timeout: DefaultTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Binary reports the configured git executable.
func (r *Runner) Binary() string { return r.bin }

// Timeout reports the configured per-invocation timeout.
func (r *Runner) Timeout() time.Duration { return r.timeout }

// scrub is the set of variables that would redirect git at another repository.
var scrub = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// Env returns the environment used for every invocation.
func (r *Runner) Env() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(r.extraEnv)+4)
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		key := strings.ToUpper(kv[:i])
		skip := false
		for _, s := range scrub {
			if key == s {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
		"LANG=C",
	)
	env = append(env, r.extraEnv...)
	return env
}

// globalArgs are prepended to every invocation so that output stays machine
// readable regardless of the user's git config.
var globalArgs = []string{
	"-c", "core.quotepath=false",
	"-c", "color.ui=false",
	"-c", "log.showSignature=false",
	"-c", "advice.detachedHead=false",
}

// Run executes git in dir with the given arguments.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	return r.run(ctx, dir, nil, args)
}

// RunInput executes git in dir, feeding stdin to the process.
func (r *Runner) RunInput(ctx context.Context, dir string, stdin []byte, args ...string) (Result, error) {
	return r.run(ctx, dir, stdin, args)
}

func (r *Runner) run(ctx context.Context, dir string, stdin []byte, args []string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if r.timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			ctx, cancel = context.WithTimeout(ctx, r.timeout)
			defer cancel()
		}
	}

	argv := make([]string, 0, len(globalArgs)+len(args))
	argv = append(argv, globalArgs...)
	argv = append(argv, args...)

	name := strings.Join(args, " ")

	if r.exec != nil {
		res, err := r.exec(ctx, dir, stdin, argv)
		if err == nil {
			return res, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return res, fmt.Errorf("gitx: git %s: %w", name, ctxErr)
		}
		var ce *CommandError
		if errors.As(err, &ce) {
			return res, err // already carries the exit code and the two streams
		}
		return res, fmt.Errorf("gitx: git %s: %w", name, err)
	}

	cmd := exec.CommandContext(ctx, r.bin, argv...)
	cmd.WaitDelay = 2 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = r.Env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err == nil {
		return res, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("gitx: git %s: %w", name, ctxErr)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &CommandError{Args: args, Code: res.ExitCode, Stderr: res.Stderr, Stdout: res.Stdout}
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) && r.bin != "" {
		return res, fmt.Errorf("gitx: git binary %q not found: %w", r.bin, err)
	}
	return res, fmt.Errorf("gitx: git %s: %w", name, err)
}

// Config is the wiring surface for cmd/piforged. The zero value is valid and
// yields the platform git binary with DefaultTimeout.
type Config struct {
	// GitBinary overrides the git executable (default "git").
	GitBinary string
	// Timeout bounds a single git invocation (default DefaultTimeout).
	Timeout time.Duration
	// Env holds extra KEY=VALUE pairs for every invocation.
	Env []string
	// Exec runs git through another machine (see WithExecutor). Nil runs it here.
	Exec ExecFunc
}

// New builds a Runner from a Config. *Runner implements the git half of
// contracts.Workspace (see GitWorkspace) and is what fsx.New should receive.
func New(cfg Config) *Runner {
	opts := make([]Option, 0, 4)
	if strings.TrimSpace(cfg.GitBinary) != "" {
		opts = append(opts, WithBinary(cfg.GitBinary))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, WithTimeout(cfg.Timeout))
	}
	if len(cfg.Env) > 0 {
		opts = append(opts, WithEnv(cfg.Env...))
	}
	if cfg.Exec != nil {
		opts = append(opts, WithExecutor(cfg.Exec))
	}
	return NewRunner(opts...)
}

// GitWorkspace is exactly the git half of contracts.Workspace. It exists so
// that the compiler can prove *Runner is wireable into the workspace.
type GitWorkspace interface {
	Status(ctx context.Context, root string) (*contracts.GitStatus, error)
	Diff(ctx context.Context, root string, req contracts.DiffRequest) ([]contracts.FileDiff, error)
	Log(ctx context.Context, root string, limit int, cursor string) (*contracts.GitGraph, error)
	Stage(ctx context.Context, root string, paths []string) error
	Unstage(ctx context.Context, root string, paths []string) error
	Commit(ctx context.Context, root string, req contracts.CommitRequest) (string, error)
	CreateBranch(ctx context.Context, root, name string) error
	Checkout(ctx context.Context, root, name string) error
}

var _ GitWorkspace = (*Runner)(nil)

// runStdout executes git in repo and returns stdout, wrapping failures.
func (r *Runner) runStdout(ctx context.Context, repo string, args ...string) (string, error) {
	res, err := r.Run(ctx, repo, args...)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}
