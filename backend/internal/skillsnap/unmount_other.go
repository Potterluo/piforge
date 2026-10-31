//go:build !linux

package skillsnap

// defaultUnmount is a no-op off Linux: the copy mechanism there is not a mount,
// so there is nothing to detach before removing the directory.
func defaultUnmount(string) error { return nil }
