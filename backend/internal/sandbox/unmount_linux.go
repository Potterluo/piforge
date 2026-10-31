//go:build linux

package sandbox

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// unmountSkills detaches the read-only bind mounts FR-14.2 puts under
// `<checkout>/.pi/skills` before the directory is deleted.
//
// A live mount makes its mount point busy: `os.RemoveAll` returns EBUSY and the
// sandbox (and therefore the project) becomes undeletable. Lazy detach is the
// same call materialize uses for a stale mount, so a failed earlier unmount is
// cleaned here too.
//
// The walk is recursive on purpose: a sandbox holds the primary checkout *and*
// one linked worktree per branch session, and every one of them has its own
// `.pi/skills`. An earlier version only looked at `<root>/.pi/skills`, so a
// project with a branch session still failed to delete while its skill mount was
// attached.
func unmountSkills(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if filepath.Base(p) != "skills" || filepath.Base(filepath.Dir(p)) != ".pi" {
			return nil
		}
		entries, rerr := os.ReadDir(p)
		if rerr != nil {
			return nil
		}
		for _, entry := range entries {
			_ = syscall.Unmount(filepath.Join(p, entry.Name()), syscall.MNT_DETACH)
		}
		// Nothing below a skills directory is a mount of ours; the entries were
		// handled above.
		return filepath.SkipDir
	})
}
