//go:build !linux

package materialize

import "errors"

// errBindUnsupported is the explicit statement that this platform has no bind
// mount in the product. Windows has no unprivileged bind mount at all, and macOS
// would need mount(2) as root for a directory the process does not own.
var errBindUnsupported = errors.New("bind mounts are not available on this platform")

func bindReadOnly(string, string) error { return errBindUnsupported }

// unmountPath has nothing to undo where nothing was mounted.
func unmountPath(string) error { return errBindUnsupported }
