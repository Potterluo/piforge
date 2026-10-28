//go:build windows

package browser

// runningAsRoot is meaningless on Windows; the sandbox flag stays off.
func runningAsRoot() bool { return false }
