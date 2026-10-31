//go:build linux

package sandbox

import (
	"path/filepath"
	"syscall"
)

// unmountSkills detaches the read-only bind mounts FR-14.2 puts under
// `<root>/.pi/skills` before the directory is deleted.
//
// A live mount makes its mount point busy: `os.RemoveAll` returns EBUSY and the
// sandbox (and therefore the project) becomes undeletable. Lazy detach is the
// same call materialize uses for a stale mount, so a failed earlier unmount is
// cleaned here too.
func unmountSkills(root string) {
	matches, err := filepath.Glob(filepath.Join(root, ".pi", "skills", "*"))
	if err != nil {
		return
	}
	for _, mount := range matches {
		_ = syscall.Unmount(mount, syscall.MNT_DETACH)
	}
}
