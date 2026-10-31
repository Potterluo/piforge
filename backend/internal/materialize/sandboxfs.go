package materialize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
)

// The sandbox's file API as a FileSystem.
//
// This is stage 2 of docs/EXECUTION-BOUNDARY.md: with -agent-in-sandbox the
// workspace is a volume of the sandbox's own, and the control plane never mounts
// it. Writing the skill projection therefore cannot be a file write here — it is
// a request to the sandbox, made as the account the agent runs as, which is also
// the account that owns the tree.
//
// What that costs, stated plainly:
//
//   - every file is one HTTP request, so a bundle of N files costs N round trips
//     once per provisioning pass. Bundles are small; a hot path this is not.
//   - there is no bind mount. A container's mount namespace is inside the
//     container, and the only thing this process can reach is the API, so the
//     read-only mechanism is a read-only copy ("copy-ro") and the session's
//     record says so. The guarantee is the mode bits, which is exactly what
//     copy-ro already meant on the host.
//   - the read-only probe is run *in the sandbox*, as the sandbox user, rather
//     than by this process: "the agent cannot write it" is a claim about that
//     account, and asking it there is the only honest answer.

// SandboxConfig configures the file API filesystem.
type SandboxConfig struct {
	// Client speaks to one sandbox's API. Required.
	Client *aio.Client
	// Root is the workspace root inside the container ("/workspace"). It is the
	// working directory of every command this filesystem runs, so it must be a
	// directory that exists; the client prefixes commands with `cd` into it.
	Root string
	// Timeout bounds one shell command. Zero uses DefaultCommandTimeout. File
	// writes and reads use the client's own timeout.
	Timeout time.Duration
}

// DefaultCommandTimeout bounds one `mkdir`/`chmod`/`rm`/probe inside the sandbox.
// These are metadata operations on a small tree; a sandbox that cannot finish one
// in this window is not slow, it is broken.
const DefaultCommandTimeout = 2 * time.Minute

// SandboxFS implements FileSystem over a sandbox's own file API.
type SandboxFS struct {
	cfg SandboxConfig
}

var _ FileSystem = (*SandboxFS)(nil)

// NewSandboxFS builds the filesystem. It panics on a missing client or root:
// both are wiring mistakes, not request-time conditions.
func NewSandboxFS(cfg SandboxConfig) (*SandboxFS, error) {
	if cfg.Client == nil {
		return nil, errors.New("materialize: the sandbox filesystem needs a client")
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("materialize: the sandbox filesystem needs the workspace root inside the container")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultCommandTimeout
	}
	return &SandboxFS{cfg: cfg}, nil
}

func (s *SandboxFS) Join(elems ...string) string { return path.Join(elems...) }

func (s *SandboxFS) IsAbs(p string) bool { return strings.HasPrefix(strings.TrimSpace(p), "/") }

// Abs is a no-op: a container path is already absolute, and filepath.Abs here
// would rewrite "/workspace/repo" into a Windows path this process cannot even
// open.
func (s *SandboxFS) Abs(p string) (string, error) { return p, nil }

func (s *SandboxFS) MkdirAll(dir string) error {
	return s.run("mkdir -p -- " + aio.Quote(dir))
}

// WriteFile writes one file. The image creates missing parents for
// /v1/file/write, which is what makes a per-file projection possible at all.
//
// mode is ignored: the image writes 0644, and the read-only pass is an explicit
// MakeReadOnly call, so a mode argument here would be a promise this filesystem
// cannot keep.
func (s *SandboxFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	if err := s.cfg.Client.WriteBytes(ctx, p, data); err != nil {
		return fmt.Errorf("materialize: write %s: %w", p, err)
	}
	return nil
}

// ReadFile reads a file's raw bytes through the download endpoint.
//
// Not /v1/file/read: that one decodes UTF-8 and would fail on the first byte of
// an image inside a skill bundle, and the fingerprint this is used for is over
// bytes.
func (s *SandboxFS) ReadFile(p string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	body, _, err := s.cfg.Client.OpenFile(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("materialize: read %s: %w", p, err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("materialize: read %s: %w", p, err)
	}
	return data, nil
}

func (s *SandboxFS) Lookup(p string) (Kind, error) {
	quoted := aio.Quote(p)
	out, err := s.output(`if [ -f ` + quoted + ` ]; then printf file; elif [ -d ` + quoted + ` ]; then printf dir; else printf missing; fi`)
	if err != nil {
		return KindMissing, err
	}
	switch strings.TrimSpace(out) {
	case "file":
		return KindFile, nil
	case "dir":
		return KindDir, nil
	default:
		return KindMissing, nil
	}
}

func (s *SandboxFS) List(dir string) ([]Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	nodes, err := s.cfg.Client.List(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("materialize: list %s: %w", dir, err)
	}
	out := make([]Entry, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, Entry{Name: node.Name, IsDir: node.IsDir})
	}
	return out, nil
}

// RemoveAll removes a tree. The write bits are cleared first: a frozen copy is
// 0555/0444 and `rm -rf` as its owner still needs to be able to unlink inside a
// directory, which a 0555 directory does not allow.
func (s *SandboxFS) RemoveAll(p string) error {
	return s.run("chmod -R u+w -- " + aio.Quote(p) + " 2>/dev/null; rm -rf -- " + aio.Quote(p))
}

// MakeReadOnly clears the write bits and probes the result in the sandbox, as
// the account that will read the tree.
//
// The probe targets SKILL.md when it exists (the file the agent would edit) and
// the directory otherwise. `test -w` is the shell's own answer for the effective
// account, which is what the mechanism badge claims.
func (s *SandboxFS) MakeReadOnly(dir string) (bool, error) {
	quoted := aio.Quote(dir)
	file := aio.Quote(s.Join(dir, "SKILL.md"))
	script := "chmod -R a-w -- " + quoted +
		"; if [ -f " + file + " ]; then probe=" + file + "; else probe=" + quoted + "; fi" +
		`; if [ -w "$probe" ]; then printf writable; else printf readonly; fi`
	out, err := s.output(script)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "writable", nil
}

func (s *SandboxFS) MakeWritable(dir string) error {
	return s.run("chmod -R u+w -- " + aio.Quote(dir))
}

// CanBind is false: a bind mount lives in the container's mount namespace, and
// the only door this process has is the sandbox's file API.
func (s *SandboxFS) CanBind() bool { return false }

// run executes one command in the sandbox and fails on a non-zero exit.
func (s *SandboxFS) run(command string) error {
	_, err := s.output(command)
	return err
}

// output executes one command in the sandbox and returns its output.
func (s *SandboxFS) output(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	res, err := s.cfg.Client.Exec(ctx, command, s.cfg.Root)
	if err != nil {
		return "", fmt.Errorf("materialize: sandbox shell: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("materialize: sandbox shell: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return res.Output, nil
}
