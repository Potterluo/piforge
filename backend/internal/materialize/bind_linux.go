//go:build linux

package materialize

import (
	"fmt"
	"syscall"
)

// bindReadOnly mounts source at target and remounts it read-only.
//
// The remount is where read-only actually happens: a bind mount alone inherits
// the source's writability, so a bundle directory the sandbox can write to would
// stay writable through the mount. If the remount fails the mount is detached
// again rather than left writable, and the caller falls back to a copy.
func bindReadOnly(source, target string) error {
	if err := syscall.Mount(source, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind mount: %w", err)
	}
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_REC)
	if err := syscall.Mount("", target, "", flags, ""); err != nil {
		_ = syscall.Unmount(target, syscall.MNT_DETACH)
		return fmt.Errorf("remount read-only: %w", err)
	}
	return nil
}

// unmountPath detaches target. MNT_DETACH is deliberate: a stale mount must not
// make provisioning fail, and a lazy detach removes it even if a process still
// has a file open.
func unmountPath(target string) error {
	return syscall.Unmount(target, syscall.MNT_DETACH)
}
