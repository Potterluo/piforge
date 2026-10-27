// Package isolate turns a command into one that runs inside a jail.
//
// The control plane starts three kinds of process: the agent, the shells a user
// opens in the terminal panel, and the sandbox's browser. All three execute code
// the platform does not trust, so all three take the same optional wrapper.
//
// Only launchers that exist as real programs are modelled here. A wrapper is
// built from an explicit argv, never from a shell string, and anything the
// launcher does not understand is rejected at construction time rather than
// surfacing as a sandbox that silently is not one.
package isolate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Launcher rewrites an argv so the program runs inside a jail.
type Launcher interface {
	// Wrap returns the argv to execute. A nil receiver and a nil Launcher both
	// mean "run it directly".
	Wrap(argv []string) []string
	// Name identifies the launcher in logs and in the API's sandbox reports.
	Name() string
}

// None runs commands with no isolation. It is the default because a laptop has
// no bwrap, and pretending otherwise would break every local run.
type None struct{}

func (None) Wrap(argv []string) []string { return argv }
func (None) Name() string                { return "none" }

// Bubblewrap wraps a command with bubblewrap: a user namespace, read-only
// system paths, and a writable bind for exactly the directories the process
// needs. bwrap is the reference implementation for "unprivileged sandbox" on
// Linux and needs no daemon.
type Bubblewrap struct {
	// Binary overrides the executable path.
	Binary string
	// WritableDirs are the only host paths the process may write.
	WritableDirs []string
	// ReadOnlyDirs are bound read-only. Empty means the usual system paths.
	ReadOnlyDirs []string
	// ShareNetwork keeps the network namespace. The agent needs it to reach the
	// model gateway and any MCP server; false gives full network isolation.
	ShareNetwork bool
}

func (b Bubblewrap) Name() string { return "bubblewrap" }

func (b Bubblewrap) Wrap(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	binary := b.Binary
	if binary == "" {
		binary = "bwrap"
	}

	readOnly := b.ReadOnlyDirs
	if len(readOnly) == 0 {
		readOnly = []string{"/usr", "/bin", "/lib", "/lib64", "/etc/resolv.conf", "/etc/ssl"}
	}

	out := []string{binary,
		// A private /tmp and /proc: two agents sharing /tmp is an information
		// leak that no amount of path validation in the API can fix.
		"--tmpfs", "/tmp",
		"--proc", "/proc",
		"--dev", "/dev",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--new-session",
	}
	if !b.ShareNetwork {
		out = append(out, "--unshare-net")
	}
	for _, dir := range readOnly {
		out = append(out, "--ro-bind-try", dir, dir)
	}
	for _, dir := range b.WritableDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		out = append(out, "--bind", dir, dir)
	}
	// --chdir after the binds so the working directory exists inside the jail.
	out = append(out, "--")
	return append(out, argv...)
}

// Firejail wraps a command with firejail, which is the pragmatic choice on
// distributions that ship it by default.
type Firejail struct {
	Binary       string
	WritableDirs []string
	ShareNetwork bool
}

func (f Firejail) Name() string { return "firejail" }

func (f Firejail) Wrap(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	binary := f.Binary
	if binary == "" {
		binary = "firejail"
	}
	out := []string{binary, "--quiet", "--private-tmp", "--private-dev", "--noroot"}
	if !f.ShareNetwork {
		out = append(out, "--net=none")
	}
	for _, dir := range f.WritableDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		out = append(out, "--whitelist="+dir)
	}
	out = append(out, "--")
	return append(out, argv...)
}

// Options is the operator-facing configuration.
type Options struct {
	// Kind is none|bubblewrap|firejail.
	Kind string
	// Binary overrides the launcher's executable.
	Binary string
	// WritableDirs are the paths the jailed process may write.
	WritableDirs []string
	// ShareNetwork allows network access from inside the jail.
	ShareNetwork bool
}

// New builds a launcher from configuration.
//
// An unknown kind is an error rather than a fallback to None: an operator who
// typed the wrong launcher name must not end up running unsandboxed code while
// believing the opposite.
func New(opts Options) (Launcher, error) {
	kind := strings.ToLower(strings.TrimSpace(opts.Kind))
	switch kind {
	case "", "none":
		return None{}, nil
	case "bubblewrap", "bwrap":
		if runtime.GOOS != "linux" {
			return nil, fmt.Errorf("isolate: bubblewrap is only available on linux (running on %s)", runtime.GOOS)
		}
		if err := checkBinary(opts.Binary, "bwrap"); err != nil {
			return nil, err
		}
		return Bubblewrap{
			Binary:       opts.Binary,
			WritableDirs: opts.WritableDirs,
			ShareNetwork: opts.ShareNetwork,
		}, nil
	case "firejail":
		if runtime.GOOS != "linux" {
			return nil, fmt.Errorf("isolate: firejail is only available on linux (running on %s)", runtime.GOOS)
		}
		if err := checkBinary(opts.Binary, "firejail"); err != nil {
			return nil, err
		}
		return Firejail{
			Binary:       opts.Binary,
			WritableDirs: opts.WritableDirs,
			ShareNetwork: opts.ShareNetwork,
		}, nil
	default:
		return nil, fmt.Errorf("isolate: unknown launcher %q (want none, bubblewrap or firejail)", opts.Kind)
	}
}

// checkBinary makes a misconfiguration loud at startup instead of at the first
// request, when it would look like a sandbox failure.
func checkBinary(override, fallback string) error {
	candidate := strings.TrimSpace(override)
	if candidate != "" {
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			return fmt.Errorf("isolate: launcher binary %q is not executable", candidate)
		}
		return nil
	}
	if _, err := lookPath(fallback); err != nil {
		return fmt.Errorf("isolate: %q was not found on PATH", fallback)
	}
	return nil
}

// lookPath is a variable so tests can pretend a binary exists.
var lookPath = func(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		if info, err := os.Stat(name); err != nil || info.IsDir() {
			return "", fmt.Errorf("not found")
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("not found")
}
