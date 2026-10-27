package fsx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscape is returned for any path that would leave the workspace root.
var ErrEscape = errors.New("fsx: path escapes workspace root")

// ErrNotExist / ErrIsDir / ErrBinary are returned by ReadFile.
var (
	ErrNotExist = errors.New("fsx: no such file")
	ErrIsDir    = errors.New("fsx: path is a directory")
)

// CleanRel validates p as a workspace-relative path and returns it cleaned
// with the platform separator. An empty result means "the root itself".
//
// Rejected: NUL bytes, absolute paths (POSIX, Windows drive and UNC forms),
// rooted paths ("/etc/passwd", `\Windows`) and anything that climbs out of the
// root after cleaning ("../../etc/passwd", "a/../../b").
func CleanRel(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrEscape, p)
	}
	if strings.TrimSpace(p) == "" {
		return "", nil
	}
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", fmt.Errorf("%w: %q is absolute", ErrEscape, p)
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("%w: %q is rooted", ErrEscape, p)
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q climbs out of the root", ErrEscape, p)
	}
	return clean, nil
}

// IsInside reports whether p is root or lives below it. The comparison folds
// case on Windows only.
func IsInside(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	if filepath.Separator == '\\' {
		r := strings.ToLower(strings.TrimSuffix(root, sep))
		q := strings.ToLower(p)
		return strings.HasPrefix(q, r+sep)
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

// Resolve turns a caller-supplied workspace-relative path into an absolute
// path that is guaranteed to stay inside root.
//
// Beyond lexical cleaning it also resolves symlinks: a path that is a symlink
// (or whose parent directory is a symlink) pointing outside root is rejected
// even though it looks lexically safe. Nonexistent targets are allowed — a
// WriteFile creates them — but their nearest existing ancestor must still
// resolve inside root.
func Resolve(root, rel string) (string, error) {
	rootAbs, err := absRoot(root)
	if err != nil {
		return "", err
	}
	clean, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(rootAbs, clean)
	if !IsInside(rootAbs, abs) {
		return "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	// Resolve the root itself so a symlinked workspace root still works.
	realRoot, err := evalOrExisting(rootAbs)
	if err != nil {
		return "", fmt.Errorf("fsx: resolve root %q: %w", root, err)
	}
	real, err := evalOrExisting(abs)
	if err != nil {
		return "", fmt.Errorf("fsx: resolve %q: %w", rel, err)
	}
	if !IsInside(realRoot, real) {
		return "", fmt.Errorf("%w: %q resolves to %q, outside %q", ErrEscape, rel, real, realRoot)
	}
	return abs, nil
}

// Rel converts an absolute path back to a workspace-relative slash path.
func Rel(root, abs string) (string, error) {
	rootAbs, err := absRoot(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if !IsInside(rootAbs, abs) {
		return "", fmt.Errorf("%w: %q is outside %q", ErrEscape, abs, rootAbs)
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", fmt.Errorf("fsx: rel: %w", err)
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// absRoot makes root absolute and refuses an empty value.
func absRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("fsx: empty workspace root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("fsx: abs %q: %w", root, err)
	}
	return filepath.Clean(abs), nil
}

// evalOrExisting resolves symlinks for p, falling back to its nearest existing
// ancestor when p itself does not exist yet.
func evalOrExisting(p string) (string, error) {
	cur := filepath.Clean(p)
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if cur == filepath.Clean(p) {
				return real, nil
			}
			// Re-attach the tail that does not exist yet. The tail is lexically
			// clean (no ".."), so joining cannot escape.
			tail, terr := filepath.Rel(cur, p)
			if terr != nil {
				return real, nil
			}
			return filepath.Join(real, tail), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		cur = parent
	}
}

// guardWrite rejects mutations of the repository metadata or of the root.
func guardWrite(rootAbs, abs, rel string) error {
	if abs == rootAbs {
		return fmt.Errorf("fsx: refusing to modify the workspace root itself")
	}
	relSlash := filepath.ToSlash(rel)
	if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
		return fmt.Errorf("fsx: refusing to modify git metadata (%q)", relSlash)
	}
	if !IsInside(rootAbs, abs) {
		return fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	return nil
}
