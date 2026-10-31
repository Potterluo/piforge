//go:build linux

package skillsnap

import "syscall"

// defaultUnmount lazily detaches a directory. MNT_DETACH is deliberate: a
// snapshot that is still the source of a live mount must not stop the delete
// from being retried, and the kernel removes it once the last reference goes.
// A directory that was never a mount point answers EINVAL, which is ignored by
// the caller.
func defaultUnmount(target string) error {
	return syscall.Unmount(target, syscall.MNT_DETACH)
}
