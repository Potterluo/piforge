//go:build windows

package tenantfs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Windows is why this file exists.
//
// `filepath.EvalSymlinks` and `os.Lstat` describe a *symbolic link* correctly and
// a *junction* not at all: Go's Lstat on a junction reports a regular file with
// mode 0666 and EvalSymlinks returns the junction's own path unchanged, while
// opening it transparently reaches the target directory. Adversarial round 5
// (e2e/.artifacts/adversarial5/REPORT-adversarial5-git-and-paths.md, findings 2
// and 3) used exactly that: a junction inside an import root returned the
// outside repository's files.
//
// The two primitives below are the ones the platform actually offers:
// GetFileAttributesW reports FILE_ATTRIBUTE_REPARSE_POINT for a junction as well
// as for a symlink, and GetFinalPathNameByHandleW on an *open* handle reports
// where the filesystem really resolved it — which is the only answer that cannot
// be fooled by a name.

var (
	procGetFinalPathNameByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")
)

// isReparsePoint reports whether p itself is a reparse point: a symbolic link, a
// junction, or any other filesystem filter that redirects the name.
func isReparsePoint(p string) (bool, error) {
	ptr, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return false, err
	}
	attrs, aerr := syscall.GetFileAttributes(ptr)
	if aerr != nil {
		return false, aerr
	}
	return attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// realPathOf is where the filesystem resolves p, following junctions and links.
//
// It opens the longest existing prefix of p and asks the handle, so a path that
// does not exist yet still gets its existing ancestors resolved — the case a
// destination path and a to-be-created file both need. The non-existent tail is
// appended lexically: it is clean, and CleanRel has already refused `..`.
func realPathOf(p string) (string, error) {
	cur := filepath.Clean(p)
	var tail []string
	for {
		real, err := resolveExisting(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return filepath.Clean(real), nil
		}
		if !os.IsNotExist(err) && !isNotDir(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Nothing on the path exists (a drive that is not mounted): answer
			// lexically rather than failing a check the caller can still use.
			return filepath.Clean(p), nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// resolveExisting opens p and reports the final path of its handle.
func resolveExisting(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return finalPath(syscall.Handle(f.Fd()))
}

// finalPath calls GetFinalPathNameByHandleW and strips the \\?\ prefix it
// returns (\\?\UNC\server\share becomes \\server\share).
func finalPath(h syscall.Handle) (string, error) {
	size := 512
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]uint16, size)
		n, _, callErr := procGetFinalPathNameByHandle.Call(
			uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return "", callErr
		}
		if int(n) < len(buf) {
			return trimDevicePrefix(syscall.UTF16ToString(buf[:n])), nil
		}
		// The buffer was one short: ask again with room for the terminator.
		size = int(n) + 1
	}
	return "", syscall.ERROR_BUFFER_OVERFLOW
}

// trimDevicePrefix removes the \\?\ namespace marker GetFinalPathNameByHandle
// returns, so the answer compares with the paths the rest of the program builds.
func trimDevicePrefix(p string) string {
	const unc = `\\?\UNC\`
	if strings.HasPrefix(p, unc) {
		return `\\` + p[len(unc):]
	}
	if strings.HasPrefix(p, `\\?\`) {
		return p[len(`\\?\`):]
	}
	return p
}

// isNotDir reports the "a component is not a directory" error, which is a
// refusal-worthy path rather than a missing file.
func isNotDir(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not a directory") ||
		strings.Contains(strings.ToLower(err.Error()), "the directory name is invalid")
}
