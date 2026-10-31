package materialize

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Kind is what Lookup found at a path.
type Kind int

const (
	// KindMissing is "there is nothing here", which is not an error.
	KindMissing Kind = iota
	// KindFile is a regular file (or a symlink to one).
	KindFile
	// KindDir is a directory.
	KindDir
)

// Entry is one name in a directory listing.
type Entry struct {
	Name  string
	IsDir bool
}

// FileSystem is the tree a mount writes into.
//
// It exists because stage 2 of docs/EXECUTION-BOUNDARY.md takes the projection
// off the shared volume. With -agent-in-sandbox the workspace lives only inside
// the sandbox: the control plane has no directory it can open, so every write,
// read, listing and mode change has to cross the sandbox's own file API
// (internal/aio). Everything that decides *what* to project — which skills, the
// freeze rule, the manifest, the git exclude, the read-only guarantee — is the
// same in both placements, so it lives once and this interface is the only part
// that differs.
//
// Join/IsAbs are here because a container path is POSIX whatever the control
// plane runs on: filepath.Join would turn "/workspace/repo" into
// "\workspace\repo" on a Windows host, and the sandbox would then be asked to
// write a path it has never heard of.
type FileSystem interface {
	// Join joins path elements in this filesystem's syntax.
	Join(elems ...string) string
	// IsAbs reports whether p is absolute in this filesystem's syntax.
	IsAbs(p string) bool
	// Abs resolves p against the filesystem's root (a no-op for a filesystem
	// whose paths are already absolute container paths).
	Abs(p string) (string, error)
	// MkdirAll creates dir and any missing parents.
	MkdirAll(dir string) error
	// WriteFile writes one file, creating missing parents. mode is honoured where
	// the filesystem has mode bits; a filesystem that does not is expected to
	// write 0644 and say so in its documentation.
	WriteFile(p string, data []byte, mode os.FileMode) error
	// ReadFile reads one file's bytes. It must not decode them: a skill bundle
	// can contain a PNG, and the fingerprint is over bytes.
	ReadFile(p string) ([]byte, error)
	// Lookup reports what is at p.
	Lookup(p string) (Kind, error)
	// List returns the entries of one directory, without "." and "..".
	List(dir string) ([]Entry, error)
	// RemoveAll removes a tree, clearing read-only bits if that is what stands in
	// the way. Removing something that is not there is not an error.
	RemoveAll(p string) error
	// MakeReadOnly sets the read-only mode bits recursively and reports whether
	// the account the agent runs as can still write the tree. The report is a
	// probe, not an assumption: permission bits do not bind a privileged account,
	// and the mechanism this package records has to be the true one.
	MakeReadOnly(dir string) (writable bool, err error)
	// MakeWritable clears the read-only bits recursively, so a stale copy can be
	// replaced.
	MakeWritable(dir string) error
	// CanBind reports whether this filesystem can be the target of a bind mount.
	// The host can (with the capability) and the sandbox file API cannot: a bind
	// mount needs a mount namespace, which is inside the container, not outside
	// it.
	CanBind() bool
}

// Binder is the optional half of FileSystem that can pin a directory to a
// read-only bind mount of another one.
//
// Only the host implementation is one. The container placement reports "copy-ro"
// instead — that is not a degradation of the guarantee, it is what the sandbox
// API can express, and the mechanism recorded for the session says so.
type Binder interface {
	BindReadOnly(source, target string) error
}

// ---------------------------------------------------------------------------
// The host filesystem
// ---------------------------------------------------------------------------

// hostFS is the control plane's own disk, which is where the projection went
// before stage 2 and where it still goes for -sandbox local and for a
// deployment that shares the workspace volume.
type hostFS struct{ opts Options }

var (
	_ FileSystem = hostFS{}
	_ Binder     = hostFS{}
)

func (h hostFS) Join(elems ...string) string { return filepath.Join(elems...) }
func (h hostFS) IsAbs(p string) bool         { return filepath.IsAbs(p) }
func (h hostFS) Abs(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("materialize: resolve %s: %w", p, err)
	}
	return abs, nil
}

func (h hostFS) MkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

func (h hostFS) WriteFile(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, mode)
}

func (h hostFS) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

func (h hostFS) Lookup(p string) (Kind, error) {
	info, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return KindMissing, nil
		}
		return KindMissing, err
	}
	if info.IsDir() {
		return KindDir, nil
	}
	return KindFile, nil
}

func (h hostFS) List(dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		out = append(out, Entry{Name: item.Name(), IsDir: item.IsDir()})
	}
	return out, nil
}

func (h hostFS) RemoveAll(p string) error { return clearMountedTree(p, h.opts) }

// MakeReadOnly marks the tree 0444/0555 and probes it the way the mechanism has
// always been decided on the host: opening SKILL.md for writing (without
// truncating) succeeds only for a process the bits do not bind.
func (h hostFS) MakeReadOnly(dir string) (bool, error) {
	if err := chmodTree(dir, true); err != nil {
		return false, err
	}
	return mountedFileIsWritable(dir), nil
}

func (h hostFS) MakeWritable(dir string) error { return chmodTree(dir, false) }

func (h hostFS) CanBind() bool { return platformPlan(h.opts).CanBind }

func (h hostFS) BindReadOnly(source, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	bind := h.opts.BindMount
	if bind == nil {
		bind = bindReadOnly
	}
	return bind(source, target)
}

// safeJoin joins base and rel, refusing anything that escapes base. rel is
// cleaned as a POSIX path first, so a bundle_key or a skill slug can never walk
// out of the tree in either placement.
func safeJoin(fs FileSystem, base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("materialize: base is required")
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	return fs.Join(base, clean), nil
}
