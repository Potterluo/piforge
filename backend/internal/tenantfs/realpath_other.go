//go:build !windows

package tenantfs

import (
	"os"
	"path/filepath"
)

// On a POSIX system the two questions have one answer each: a symlink is the
// reparse point (there is nothing else that redirects a name), and EvalSymlinks
// resolves it. A *bind mount* is the Windows junction's cousin and is invisible
// to both — a bind mount inside an import root still resolves to the outside
// tree. That is stated as a residual in docs/DATA-FLOW-AUDIT.md §11 rather than
// pretended away; the deployment's import roots are the control.

// isReparsePoint reports whether p is a symbolic link.
func isReparsePoint(p string) (bool, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// realPathOf resolves p, falling back to its nearest existing ancestor so a path
// that does not exist yet is still resolved as far as it can be.
func realPathOf(p string) (string, error) {
	cur := filepath.Clean(p)
	var tail []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return filepath.Clean(real), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(p), nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}
