// Package skillsnap owns the per-session frozen skill snapshot directory that
// FR-14.2 creates under `<data>/skill-snapshots/<session>`.
//
// A snapshot exists so a mounted skill tree cannot change under a running
// session: a read-only bind mount's source is this directory, not the live
// bundle. That makes the directory part of a session's state, which means it has
// to disappear when the session does — and if the delete cannot finish (another
// process is holding it, or a mount is still attached), a later sweep has to be
// able to recognise it as garbage.
//
// The package is deliberately filesystem-only and has no database dependency:
// deciding *which* snapshots are garbage is the caller's job (the janitor asks
// the store for live session ids; the HTTP layer removes exactly the sessions it
// deleted).
package skillsnap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sessionDir matches the directory name of a snapshot: a session id, in the
// current layout (`ses_…`) or the layout older builds used (`ses-…`, the id's
// underscore folded to a dash by the slug rule). A name that does not look like
// one of those is somebody else's directory and is never removed.
var sessionDir = regexp.MustCompile(`^ses[-_][A-Za-z0-9]+$`)

// safeName is what may become a directory name verbatim. Session ids are
// Crockford base32 with an underscore prefix, so they always pass; the check is
// what keeps a crafted id from escaping the snapshot root.
var safeName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Freed is what one removal reclaimed, so the janitor can report count and
// bytes instead of "some directories".
type Freed struct {
	// Dirs is 1 when a session's directory was removed, 0 when there was
	// nothing to remove or the removal failed.
	Dirs int
	// Bytes is the size of the files the removal reclaimed.
	Bytes int64
}

// Dir is the snapshot directory for one session.
func Dir(root, sessionID string) string {
	return filepath.Join(root, Name(sessionID))
}

// Name is the directory name for a session's snapshot: the id itself, because
// session ids are already safe and an exact name makes "which session owns this
// directory?" answerable without a hash.
//
// A caller passing something that is not safe gets the legacy slug rule, so the
// result never escapes the root. Session ids never take that path.
func Name(sessionID string) string {
	id := strings.TrimSpace(sessionID)
	if safeName.MatchString(id) && id != "." && id != ".." {
		return id
	}
	return legacyName(id)
}

// legacyName is the pre-existing layout: lower case, runs of characters outside
// [a-z0-9] folded to one dash. It is kept so that an upgrade does not sweep a
// running session's snapshot (created by an older build) out from under its
// mount, and so a delete still finds both layouts.
func legacyName(sessionID string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(sessionID)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Names returns every directory name this package could have given a session,
// current layout first. A delete removes all of them; a sweep keeps a directory
// whose name is in LiveNames.
func Names(sessionID string) []string {
	names := []string{Name(sessionID)}
	if legacy := legacyName(sessionID); legacy != names[0] {
		names = append(names, legacy)
	}
	return names
}

// IsSessionDir reports whether a directory name has the shape of a session's
// snapshot. It is the safety boundary for the sweep: a directory an operator
// created by hand is not ours.
func IsSessionDir(name string) bool { return sessionDir.MatchString(name) }

// LiveNames is the set of directory names that belong to a live session.
func LiveNames(sessionIDs []string) map[string]bool {
	live := make(map[string]bool, 2*len(sessionIDs))
	for _, id := range sessionIDs {
		for _, name := range Names(id) {
			if name != "" {
				live[name] = true
			}
		}
	}
	return live
}

// Remove deletes every layout of one session's snapshot directory.
func Remove(root, sessionID string, unmount func(string) error) (Freed, error) {
	var total Freed
	var firstErr error
	for _, name := range Names(sessionID) {
		freed, err := RemoveDir(root, name, unmount)
		total.Dirs += freed.Dirs
		total.Bytes += freed.Bytes
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return total, firstErr
}

// RemoveDir deletes one snapshot directory by name.
//
// A snapshot that is still a mount point cannot be removed (`EBUSY`), so every
// directory under it is detached first. Detach failures are not fatal on their
// own: the RemoveAll below reports the real outcome, which the caller logs and
// retries on a later pass instead of aborting a whole sweep.
func RemoveDir(root, name string, unmount func(string) error) (Freed, error) {
	if !IsSessionDir(name) {
		return Freed{}, fmt.Errorf("skillsnap: %q is not a session snapshot directory", name)
	}
	return RemoveNamed(root, name, unmount)
}

// RemoveNamed is the same removal without the session-name check, for the other
// directories a session owns (<data>/pi-agents/<session> and
// <data>/artifacts/<session>) and for the sandbox/skill sweeps. The caller has
// already matched the name against its own pattern; keeping the mount detach and
// the read-only retry in one place is what makes every sweep behave the same way
// when a live process still holds a file.
func RemoveNamed(root, name string, unmount func(string) error) (Freed, error) {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return Freed{}, fmt.Errorf("skillsnap: %q is not a safe directory name", name)
	}
	dir := filepath.Join(root, name)
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Freed{}, nil
		}
		return Freed{}, fmt.Errorf("skillsnap: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return Freed{}, fmt.Errorf("skillsnap: %s is not a directory", dir)
	}

	bytes := measure(dir)
	detach := unmount
	if detach == nil {
		detach = defaultUnmount
	}
	detachTree(dir, detach)

	if err := os.RemoveAll(dir); err == nil {
		return Freed{Dirs: 1, Bytes: bytes}, nil
	}
	// The tree is usually a read-only copy (0444/0555) too; clear the bits and
	// retry once, exactly like the sandbox provider's own destroy.
	if cerr := chmodTree(dir, false); cerr != nil && !os.IsNotExist(cerr) {
		return Freed{}, fmt.Errorf("skillsnap: make %s writable: %w", dir, cerr)
	}
	if err := os.RemoveAll(dir); err != nil {
		return Freed{}, fmt.Errorf("skillsnap: remove %s: %w", dir, err)
	}
	return Freed{Dirs: 1, Bytes: bytes}, nil
}

// measure sums the size of the regular files under dir. It ignores errors: it
// feeds a log line, not an accounting system.
func measure(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// detachTree lazily detaches every directory under root (and root itself). A
// directory that is not a mount point answers EINVAL, which is expected and
// ignored.
func detachTree(root string, unmount func(string) error) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = unmount(dirs[i])
	}
}

// chmodTree makes a tree writable. Directories come last, deepest first, because
// a 0555 directory refuses the chmod of its entries even for its owner on some
// platforms.
func chmodTree(root string, readOnly bool) error {
	fileMode, dirMode := os.FileMode(0o644), os.FileMode(0o755)
	if readOnly {
		fileMode, dirMode = 0o444, 0o555
	}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if cerr := os.Chmod(p, fileMode); cerr != nil && !os.IsNotExist(cerr) {
			return cerr
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if cerr := os.Chmod(dirs[i], dirMode); cerr != nil && !os.IsNotExist(cerr) {
			return cerr
		}
	}
	return nil
}
