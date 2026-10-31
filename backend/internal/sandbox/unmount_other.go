//go:build !linux

package sandbox

// unmountSkills is a no-op where skills are plain copies (Windows, macOS):
// there is no mount to detach before deleting the tree.
func unmountSkills(string) {}
