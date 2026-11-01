// Package tenantfs confines every read of a filesystem tree a tenant controls.
//
// The rule it implements is the one an adversarial acceptance run asked for
// after `POST /v1/skills/import` copied `/proc/1/environ` into a skill bundle
// (e2e/.artifacts/adversarial3/REPORT.md, attack 3): a path under a
// tenant-controlled tree is read only when every component of it is a real
// directory or a real file inside that tree. A symbolic link is never followed,
// and a file whose resolved location leaves the tree is refused.
//
// The refusal is typed (Refusal) and names the offending path, because the
// caller must refuse the whole import rather than the one file: a tenant who
// ships a symlink in a repository is hostile, not unlucky, and a partial import
// would hide the fact that they tried.
//
// What this package deliberately does not do: it does not detect hard links
// (git and the zip reader cannot create them, so a tree staged by either has
// none), and it does not police writes — only reads of somebody else's tree.
//
// Windows: a *junction* is not a symbolic link. Go's Lstat reports one as a
// regular file and EvalSymlinks passes it through unchanged, while opening it
// reaches the target — which is how adversarial round 5 read a repository
// outside an import root. isReparsePoint and realPathOf (realpath_windows.go)
// are the platform's own answer: FILE_ATTRIBUTE_REPARSE_POINT on the name, and
// GetFinalPathNameByHandle on an open handle.
package tenantfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Kinds of refusal. A caller that wants to explain the refusal to a person
// switches on these; the contract is stable, the sentences are not.
const (
	// KindSymlink: a component of the path, or the file itself, is a symbolic
	// link. This is the attack: a link is a way to name a file outside the tree.
	KindSymlink = "symlink"
	// KindOutside: the path resolved (through the filesystem) to somewhere the
	// tree does not own. Belt and braces after the link checks.
	KindOutside = "outside_root"
	// KindNotRegular: the path is a directory, a device, a fifo or a socket
	// where a regular file was expected.
	KindNotRegular = "not_a_regular_file"
	// KindTooLarge: the file is larger than the caller's cap, so it was not read
	// into memory.
	KindTooLarge = "too_large"
	// KindInvalid: the relative path itself is unusable (absolute, empty, `..`,
	// NUL byte).
	KindInvalid = "invalid_path"
)

// Refusal is a typed "this path will not be read", naming the path.
//
// Path is the path relative to the tree's root, slash-separated, because that
// is what a tenant recognises in an error message ("leakskill/key.txt" rather
// than "/tmp/piforge-skills-123/leakskill/key.txt"). Absolute is carried too,
// for the operator's log.
type Refusal struct {
	Path     string
	Absolute string
	Kind     string
	Detail   string
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("tenantfs: refusing ")
	fmt.Fprintf(&b, "%q", r.Path)
	b.WriteString(": ")
	switch r.Kind {
	case KindSymlink:
		b.WriteString("it is a symbolic link")
	case KindOutside:
		b.WriteString("it resolves outside the tree")
	case KindNotRegular:
		b.WriteString("it is not a regular file")
	case KindTooLarge:
		b.WriteString("it is larger than the cap")
	case KindInvalid:
		b.WriteString("the path is not usable")
	default:
		b.WriteString(r.Kind)
	}
	if r.Detail != "" {
		b.WriteString(" (" + r.Detail + ")")
	}
	return b.String()
}

// Tree is one tenant-controlled directory, opened for confined reads.
//
// Both the lexical root and its resolved real path are kept: the lexical one is
// what paths are joined onto, the real one is the boundary every resolved path
// is compared against.
type Tree struct {
	root string
	real string
}

// Open confines root. The directory must exist: a tree that is not there is a
// caller bug, not an empty import.
func Open(root string) (*Tree, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("tenantfs: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("tenantfs: abs %q: %w", root, err)
	}
	abs = filepath.Clean(abs)
	// A root that is not there is a configuration error, not an empty tree:
	// realPathOf answers lexically for a path that does not exist, so the
	// existence check is the caller's to make and this is it.
	info, serr := os.Stat(abs)
	if serr != nil {
		return nil, fmt.Errorf("tenantfs: root %q is not there: %w", root, serr)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tenantfs: root %q is not a directory", root)
	}
	real, err := realPathOf(abs)
	if err != nil {
		return nil, fmt.Errorf("tenantfs: resolve root %q: %w", root, err)
	}
	return &Tree{root: abs, real: real}, nil
}

// Root is the lexical root the tree was opened on.
func (t *Tree) Root() string { return t.root }

// ReadFile returns the body of one file under the tree, refusing a symlink in
// any component, refusing a path that resolves outside the tree, and never
// reading more than max bytes (max <= 0 means "no per-file cap").
func (t *Tree) ReadFile(rel string, max int64) ([]byte, error) {
	clean, err := t.cleanRel(rel)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return nil, &Refusal{Path: "", Absolute: t.root, Kind: KindNotRegular,
			Detail: "the root itself is a directory"}
	}
	if err := t.refuseLinkComponents(clean); err != nil {
		return nil, err
	}
	full := filepath.Join(t.root, clean)

	// Lstat, then open, then compare: a path that was a regular file when it was
	// checked and a link when it was opened is refused rather than read. (The
	// window is small; closing it entirely needs O_NOFOLLOW, and the resolved-path
	// check below covers the same ground from the other side.)
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, t.symlinkRefusal(clean, "the file itself is a link")
	}
	if !info.Mode().IsRegular() {
		return nil, &Refusal{Path: slash(clean), Absolute: full, Kind: KindNotRegular,
			Detail: info.Mode().Type().String()}
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, t.symlinkRefusal(clean, "the path changed while it was being read")
	}
	if err := t.refuseOutside(clean, full); err != nil {
		return nil, err
	}

	if max > 0 {
		body, rerr := io.ReadAll(io.LimitReader(f, max+1))
		if rerr != nil {
			return nil, rerr
		}
		if int64(len(body)) > max {
			return nil, &Refusal{Path: slash(clean), Absolute: full, Kind: KindTooLarge,
				Detail: fmt.Sprintf("more than %d bytes", max)}
		}
		return body, nil
	}
	return io.ReadAll(f)
}

// Walk visits every entry under rel (rel == "" is the root), depth first, in
// the order the filesystem returns names. Directories named ".git" are skipped:
// they are git's own storage, not the tenant's content.
//
// It refuses the first symbolic link it meets, anywhere in the tree, rather
// than skipping it. The caller turns that into "this import is refused", which
// is the point: a link in a tenant's tree is an attempt to name a file outside
// it, whether or not the file next to it is the one that would have been read.
func (t *Tree) Walk(rel string, fn func(rel string, info os.FileInfo) error) error {
	clean, err := t.cleanRel(rel)
	if err != nil {
		return err
	}
	start := t.root
	if clean != "" {
		if err := t.refuseLinkComponents(clean); err != nil {
			return err
		}
		start = filepath.Join(t.root, clean)
	}
	return t.walkDir(start, clean, fn)
}

// WalkFiles is Walk restricted to regular files: a directory, device or fifo is
// refused instead of being handed to fn (which would then try to read it).
func (t *Tree) WalkFiles(rel string, fn func(rel string, info os.FileInfo) error) error {
	return t.Walk(rel, func(p string, info os.FileInfo) error {
		if !info.Mode().IsRegular() {
			return &Refusal{Path: slash(p), Kind: KindNotRegular,
				Detail: info.Mode().Type().String()}
		}
		return fn(p, info)
	})
}

// walkDir is the recursive half of Walk.
func (t *Tree) walkDir(dir, relDir string, fn func(rel string, info os.FileInfo) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(dir, name)
		rel := name
		if relDir != "" {
			rel = filepath.Join(relDir, name)
		}
		// Lstat rather than entry.Type(): a filesystem that does not report the
		// type in readdir must not be able to hide a link.
		info, ierr := os.Lstat(full)
		if ierr != nil {
			return ierr
		}
		// A junction is a directory to the rest of the world and a regular file
		// to Lstat; isReparsePoint is the platform's answer, and either way the
		// walk refuses rather than descends.
		reparse, rerr := isReparsePoint(full)
		if rerr != nil {
			return rerr
		}
		if reparse || info.Mode()&os.ModeSymlink != 0 {
			return t.symlinkRefusal(rel, "a link inside a tenant-controlled tree")
		}
		if info.IsDir() {
			if name == ".git" {
				continue
			}
			if werr := t.walkDir(full, rel, fn); werr != nil {
				return werr
			}
			continue
		}
		if ferr := fn(rel, info); ferr != nil {
			return ferr
		}
	}
	return nil
}

// refuseLinkComponents rejects a path whose parent directories include a link,
// a junction or any other reparse point.
func (t *Tree) refuseLinkComponents(clean string) error {
	cur := t.root
	parts := strings.Split(clean, string(filepath.Separator))
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		// reparse is the platform's answer: on Windows a junction is not a
		// symlink to Lstat and would otherwise pass as a regular file.
		reparse, rerr := isReparsePoint(cur)
		if rerr != nil {
			return rerr
		}
		if reparse || info.Mode()&os.ModeSymlink != 0 {
			detail := "a link on the path to the file"
			if i == len(parts)-1 {
				detail = "the file itself is a link"
			}
			return t.symlinkRefusal(strings.Join(parts[:i+1], string(filepath.Separator)), detail)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return &Refusal{Path: slash(strings.Join(parts[:i+1], string(filepath.Separator))),
				Absolute: cur, Kind: KindNotRegular, Detail: "a component of the path is not a directory"}
		}
	}
	return nil
}

// refuseOutside is the resolved-path half of the rule: whatever the filesystem
// says this path really is, it has to live under the resolved root. It uses the
// handle-based resolver, so a junction is followed only to be refused.
func (t *Tree) refuseOutside(clean, full string) error {
	real, err := realPathOf(full)
	if err != nil {
		return err
	}
	if !inside(t.real, real) {
		return &Refusal{Path: slash(clean), Absolute: real, Kind: KindOutside,
			Detail: "resolved to " + real}
	}
	return nil
}

func (t *Tree) symlinkRefusal(rel, detail string) error {
	full := filepath.Join(t.root, rel)
	return &Refusal{Path: slash(rel), Absolute: full, Kind: KindSymlink, Detail: detail}
}

// cleanRel validates a tree-relative path: no NUL byte, no absolute path, no
// drive or UNC form, nothing that climbs out after cleaning.
func (t *Tree) cleanRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", &Refusal{Path: rel, Kind: KindInvalid, Detail: "it contains a NUL byte"}
	}
	if strings.TrimSpace(rel) == "" || rel == "." {
		return "", nil
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", &Refusal{Path: rel, Kind: KindInvalid, Detail: "it is absolute"}
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &Refusal{Path: rel, Kind: KindInvalid, Detail: "it climbs out of the tree"}
	}
	return clean, nil
}

// Contains reports whether p resolves to root or below it.
//
// It is the "is this path one the caller already owns" half of the rule, for a
// caller that has a path in hand rather than a tree to read: a tenant-supplied
// filesystem path may be handed to a tool (git, a reader) only when it lives
// under a root the deployment wrote down. Like Tree, it resolves links *and
// junctions* — a junction inside the root naming something outside it is
// outside — and it accepts a path that does not exist yet by resolving its
// nearest existing ancestor, which is what a destination path needs.
//
// root must exist: a root that is not there is a configuration error, not an
// empty answer.
func Contains(root, p string) (bool, error) {
	_, within, err := Resolve(root, p)
	return within, err
}

// Resolve answers both halves at once: the path the filesystem really resolved p
// to, and whether that is under root.
//
// Callers must *use* the returned path rather than p. That is what closes the
// check-then-use race adversarial round 5 won two times in six (finding 3): a
// check on `imports/race` followed by `git clone imports/race` re-resolves the
// name, and the attacker flips the junction in between. A check that returns the
// resolved path lets the clone name `imports/race-real` — the thing that was
// verified — and flipping the junction afterwards changes nothing.
//
// What remains, stated rather than hidden: a component *inside* the resolved
// path can still be re-pointed between the resolution and the open (the resolved
// path is only as stable as the tree it names), and on POSIX a bind mount is not
// a reparse point at all. Both are the deployment's tree, not the tenant's.
func Resolve(root, p string) (resolved string, within bool, err error) {
	if strings.TrimSpace(root) == "" {
		return "", false, fmt.Errorf("tenantfs: empty root")
	}
	if strings.ContainsRune(p, 0) {
		return "", false, &Refusal{Path: p, Kind: KindInvalid, Detail: "it contains a NUL byte"}
	}
	rootAbs, aerr := filepath.Abs(root)
	if aerr != nil {
		return "", false, fmt.Errorf("tenantfs: abs %q: %w", root, aerr)
	}
	// A root that is not there is a configuration error, not "false" (see Open).
	if info, serr := os.Stat(rootAbs); serr != nil {
		return "", false, fmt.Errorf("tenantfs: root %q is not there: %w", root, serr)
	} else if !info.IsDir() {
		return "", false, fmt.Errorf("tenantfs: root %q is not a directory", root)
	}
	rootReal, rerr := realPathOf(filepath.Clean(rootAbs))
	if rerr != nil {
		return "", false, fmt.Errorf("tenantfs: resolve root %q: %w", root, rerr)
	}
	pAbs, perr := filepath.Abs(p)
	if perr != nil {
		return "", false, fmt.Errorf("tenantfs: abs %q: %w", p, perr)
	}
	real, eerr := realPathOf(pAbs)
	if eerr != nil {
		return "", false, fmt.Errorf("tenantfs: resolve %q: %w", p, eerr)
	}
	return real, inside(rootReal, real), nil
}

// inside reports whether p is root or below it, comparing resolved paths.
func inside(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	if filepath.Separator == '\\' {
		return strings.HasPrefix(strings.ToLower(p), strings.ToLower(strings.TrimSuffix(root, sep))+sep)
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

func slash(p string) string { return filepath.ToSlash(p) }
