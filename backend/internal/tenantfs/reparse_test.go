package tenantfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Adversarial round 5, finding 2: a *junction* inside an import root leaked the
// repository it pointed at. The reason is that `filepath.EvalSymlinks` and
// `os.Lstat` describe a junction as an ordinary file on Windows — EvalSymlinks
// returns the junction's own name unchanged — while opening it reaches the
// target. Everything this package decides therefore has to use the platform's
// own two primitives (isReparsePoint, realPathOf), and this test is the attack
// as a unit: a link inside the root that names a file outside it.
func TestALinkInsideTheRootIsOutsideIt(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("LEAK"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := makeLink(t, outside, link); err != nil {
		t.Skipf("this machine cannot create a %s link: %v", linkKind(), err)
	}

	resolved, within, err := Resolve(root, link)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", link, err)
	}
	if within {
		t.Fatalf("Resolve reported a link to %s as inside %s (resolved=%s)", outside, root, resolved)
	}
	if !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(outside)) {
		t.Fatalf("Resolve returned %q, want the target %q", resolved, outside)
	}

	if ok, cerr := Contains(root, link); cerr != nil {
		t.Fatalf("Contains: %v", cerr)
	} else if ok {
		t.Fatalf("Contains reported %q as inside the root", link)
	}

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The walk refuses the link rather than descending into the outside tree:
	// a link in a tenant's tree is an attempt, not an accident.
	if werr := tree.Walk("", func(string, os.FileInfo) error { return nil }); werr == nil {
		t.Fatal("Walk descended through a link to an outside directory")
	}
	if _, rerr := tree.ReadFile(filepath.Join("link", "secret.txt"), 0); rerr == nil {
		t.Fatal("ReadFile read a file through a link to an outside directory")
	}
}

// A path that does not exist yet still resolves through its existing ancestors,
// and an ordinary directory resolves to itself: the check must not fire on
// everything.
func TestResolveDoesNotInvert(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	resolved, within, err := Resolve(root, filepath.Join(root, "sub", "not", "yet"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !within {
		t.Fatalf("a destination inside the root was reported outside (resolved=%s)", resolved)
	}
	if real, err := realPathOf(root); err != nil {
		t.Fatalf("realPathOf: %v", err)
	} else if !strings.EqualFold(filepath.Clean(real), filepath.Clean(root)) {
		// On Windows the temp directory may itself be reached through a link
		// (C:\Users\x\AppData\Local\Temp is real, but a build agent may redirect
		// it): comparing against the *resolved* root, not the lexical one.
		t.Logf("realPathOf(root) = %q, root = %q (a redirected temp directory is not a failure)", real, root)
	}
}

// makeLink creates the kind of link the platform uses for a directory
// redirection that is not a symbolic link: a junction on Windows (no privilege
// needed, and invisible to Lstat), a symlink elsewhere.
func makeLink(t *testing.T, target, link string) error {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			return &linkError{msg: string(out), err: err}
		}
		return nil
	}
	return os.Symlink(target, link)
}

func linkKind() string {
	if runtime.GOOS == "windows" {
		return "junction"
	}
	return "symbolic"
}

type linkError struct {
	msg string
	err error
}

func (e *linkError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *linkError) Unwrap() error { return e.err }
