//go:build linux || darwin

package browser

import "os"

// runningAsRoot reports whether the browser would be started by root. Chromium
// refuses to run as root unless its own sandbox is disabled, which is the normal
// case inside a container.
func runningAsRoot() bool { return os.Geteuid() == 0 }
