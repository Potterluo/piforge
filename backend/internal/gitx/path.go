package gitx

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CleanRel validates p as a repository-relative path and returns it cleaned.
//
// It rejects NUL bytes, absolute paths (including Windows drive and UNC forms)
// and anything that escapes upwards after cleaning. An empty result means "the
// repository root itself".
func CleanRel(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrPathEscape, p)
	}
	if strings.TrimSpace(p) == "" {
		return "", nil
	}
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrPathEscape, p)
	}
	// A leading separator is a rooted path on Windows ("/etc/passwd" resolves
	// against the current volume) and an absolute path on Unix. Reject both.
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("%w: %q is rooted", ErrPathEscape, p)
	}
	// On Windows a backslash separates; on Unix it is a legal filename byte and
	// filepath.FromSlash leaves it alone, which is what we want.
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q climbs out of the repository", ErrPathEscape, p)
	}
	return clean, nil
}

// SecurePath joins rel onto root and guarantees the result stays inside root.
// root is made absolute first.
func SecurePath(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("gitx: abs %q: %w", root, err)
	}
	clean, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(rootAbs, clean), nil
}

// SecureAbsPath validates an absolute path that must stay inside root. It is
// used for worktree directories, which are passed as absolute paths.
func SecureAbsPath(root, p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrPathEscape, p)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: %q must be absolute", ErrPathEscape, p)
	}
	clean := filepath.Clean(p)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("gitx: abs %q: %w", root, err)
	}
	if !IsInside(rootAbs, clean) {
		return "", fmt.Errorf("%w: %q is outside %q", ErrPathEscape, clean, rootAbs)
	}
	return clean, nil
}

// IsInside reports whether p is root or lives below root. Both should be
// cleaned absolute paths; the comparison is case-insensitive on Windows.
func IsInside(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	// Case folding on Windows avoids rejecting C:\Repo vs c:\repo.
	if isWindows() {
		r := strings.ToLower(root)
		q := strings.ToLower(p)
		if q == r {
			return true
		}
		return strings.HasPrefix(q, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator))
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// RepoRelative converts an absolute path inside root to a slash-separated
// repository-relative path.
func RepoRelative(root, abs string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("gitx: abs %q: %w", root, err)
	}
	abs = filepath.Clean(abs)
	if !IsInside(rootAbs, abs) {
		return "", fmt.Errorf("%w: %q is outside %q", ErrPathEscape, abs, rootAbs)
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", fmt.Errorf("gitx: rel %q to %q: %w", rootAbs, abs, err)
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// safeRevArg validates a revision / commit / branch argument so it cannot be
// mistaken for an option or an option-injection.
func safeRevArg(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("gitx: empty revision")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("gitx: revision %q may not start with '-'", s)
	}
	if strings.ContainsAny(s, "\x00\r\n\t ") {
		return fmt.Errorf("gitx: revision %q contains whitespace or control characters", s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("gitx: revision %q may not contain '..'", s)
	}
	return nil
}

// isWindows is a tiny helper so path.go stays dependency free.
func isWindows() bool {
	return filepath.Separator == '\\'
}

// absClean returns the absolute, cleaned form of p.
func absClean(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("gitx: abs %q: %w", p, err)
	}
	return filepath.Clean(abs), nil
}
