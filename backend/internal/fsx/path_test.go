package fsx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPathTraversalAttacks is the core safety test of this package (PRD
// §6.3-坑6). Every attack must be rejected before any file outside the root is
// touched.
func TestPathTraversalAttacks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, outside, "secret.txt", "top secret\n")
	mustWrite(t, root, "ok.txt", "fine\n")

	ctx := context.Background()
	s := New(Config{})

	attacks := []string{
		"../../etc/passwd",
		"..%2f..",
		"../" + filepath.Base(outside) + "/secret.txt",
		"sub/../../secret.txt",
		"..",
		"C:\\Windows\\win.ini",
		"C:\\Users\\Public",
		"/etc/passwd",
		`\\server\share\file`,
		"a\x00b",
		".git/config",
	}

	for _, attack := range attacks {
		if _, err := Resolve(root, attack); err == nil {
			// "..%2f.." and ".git/config" are legal *names* (encoding is
			// decoded by the HTTP layer, and .git is a write guard), so the
			// requirement is that they cannot reach outside the root.
			abs, _ := Resolve(root, attack)
			if !IsInside(root, abs) {
				t.Errorf("Resolve(%q) = %q escaped root", attack, abs)
			}
			continue
		}
		// Every read/write entry point must reject it too.
		if _, err := s.ReadFile(ctx, root, attack); err == nil {
			t.Errorf("ReadFile(%q) should fail", attack)
		}
		if err := s.WriteFile(ctx, root, attack, []byte("pwn")); err == nil {
			t.Errorf("WriteFile(%q) should fail", attack)
		}
		if err := s.DeleteFile(ctx, root, attack); err == nil {
			t.Errorf("DeleteFile(%q) should fail", attack)
		}
	}

	// Nothing outside the root was modified.
	if body, err := os.ReadFile(filepath.Join(outside, "secret.txt")); err != nil || string(body) != "top secret\n" {
		t.Errorf("outside file modified: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "..", "pwn")); err == nil {
		t.Errorf("traversal write landed next to the root")
	}
}

// TestSymlinkEscape covers a symlink that is lexically inside the root but
// points outside it.
func TestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, outside, "secret.txt", "top secret\n")

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	if _, err := Resolve(root, "escape/secret.txt"); err == nil {
		t.Errorf("symlink traversal was allowed")
	}
	s := New(Config{})
	ctx := context.Background()
	if _, err := s.ReadFile(ctx, root, "escape/secret.txt"); err == nil {
		t.Errorf("ReadFile through symlink was allowed")
	}
	if err := s.WriteFile(ctx, root, "escape/pwn.txt", []byte("pwn")); err == nil {
		t.Errorf("WriteFile through symlink was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); err == nil {
		t.Errorf("write escaped through the symlink")
	}

	// A symlink that stays inside the root is allowed.
	insideDir := filepath.Join(root, "real")
	mustWrite(t, root, "real/file.txt", "ok\n")
	if err := os.Symlink(insideDir, filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	if _, err := s.ReadFile(ctx, root, "alias/file.txt"); err != nil {
		t.Errorf("in-root symlink should work: %v", err)
	}
}

func TestSymlinkInParentComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, outside, "nested/secret.txt", "top secret\n")

	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	if _, err := Resolve(root, "link/nested/new.txt"); err == nil {
		t.Errorf("nonexistent target behind an escaping symlink was allowed")
	}
}

func TestCleanRelRules(t *testing.T) {
	good := map[string]string{
		"":               "",
		"a.txt":          "a.txt",
		"./a.txt":        "a.txt",
		"sub/x/y.go":     filepath.Join("sub", "x", "y.go"),
		"sub/../a.txt":   "a.txt",
		"a/./b/../c.txt": filepath.Join("a", "c.txt"),
	}
	for in, want := range good {
		got, err := CleanRel(in)
		if err != nil {
			t.Errorf("CleanRel(%q) = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CleanRel(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"..",
		"../x",
		"a/../../x",
		"/abs",
		`\abs`,
		"C:\\x",
		"//server/share",
		"a\x00b",
	}
	for _, in := range bad {
		if _, err := CleanRel(in); err == nil {
			t.Errorf("CleanRel(%q) should fail", in)
		} else if !errors.Is(err, ErrEscape) {
			t.Errorf("CleanRel(%q) err = %v, want ErrEscape", in, err)
		}
	}
	if runtime.GOOS != "windows" {
		// On Windows a backslash is a separator, so this check is
		// platform-specific.
		if _, err := CleanRel(`a\b`); err != nil {
			t.Errorf("backslash in a filename should be legal on unix: %v", err)
		}
	}
}

func TestResolveRootAndRel(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "sub/file.txt", "x\n")

	abs, err := Resolve(root, "sub/file.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !IsInside(root, abs) {
		t.Errorf("resolved outside root: %q", abs)
	}
	rel, err := Rel(root, abs)
	if err != nil || rel != "sub/file.txt" {
		t.Errorf("Rel = %q, %v", rel, err)
	}
	if _, err := Rel(root, filepath.Dir(root)); err == nil {
		t.Errorf("Rel outside should fail")
	}
	if _, err := Resolve("", "x"); err == nil {
		t.Errorf("empty root should fail")
	}
	// A root that is itself a symlink still works.
	linkRoot := filepath.Join(t.TempDir(), "linkroot")
	if err := os.Symlink(root, linkRoot); err == nil {
		if _, err := Resolve(linkRoot, "sub/file.txt"); err != nil {
			t.Errorf("symlinked root: %v", err)
		}
	}
}

func TestGuardWrite(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s := New(Config{})
	for _, p := range []string{".git/config", ".git", ".git/hooks/pre-commit"} {
		if err := s.WriteFile(ctx, root, p, []byte("x")); err == nil {
			t.Errorf("WriteFile(%q) should be refused", p)
		}
	}
	// A path that merely contains ".git" is fine.
	if err := s.WriteFile(ctx, root, "docs/.gitignore.md", []byte("x")); err != nil {
		t.Errorf("ordinary path refused: %v", err)
	}
	if !strings.Contains(strings.ToLower(errText(s.WriteFile(ctx, root, ".git/x", nil))), "git metadata") {
		t.Errorf("expected a git metadata error")
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
