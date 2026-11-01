package tenantfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// write puts a file under dir, creating parents.
func write(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

// link creates a symlink, skipping the test on a machine that cannot create one.
// On Linux that never happens; on Windows it needs developer mode, and a test
// that silently passes there would be worse than one that says why it did not
// run.
func link(t *testing.T, target, linkPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir for link: %v", err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("this machine cannot create symlinks: %v", err)
	}
}

// assertRefusal checks that err is a typed Refusal of the wanted kind naming the
// wanted path.
func assertRefusal(t *testing.T, err error, kind, path string) {
	t.Helper()
	if err == nil {
		t.Fatal("the read was allowed")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a *tenantfs.Refusal", err)
	}
	if refusal.Kind != kind {
		t.Fatalf("refusal kind = %q, want %q (%v)", refusal.Kind, kind, err)
	}
	if filepath.ToSlash(refusal.Path) != path {
		t.Fatalf("refusal names %q, want %q", refusal.Path, path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("the message does not carry the path: %v", err)
	}
}

func TestReadFileReadsAnOrdinaryFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skill/SKILL.md", "hello\n")
	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	body, err := tree.ReadFile("skill/SKILL.md", 1024)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "hello\n" {
		t.Fatalf("body = %q", body)
	}
}

// The attack from the report: a skill directory whose "key" file is a link to a
// file the control plane owns. Reading it must be a typed refusal, not the
// target's bytes.
func TestReadFileRefusesALinkToAFileOutsideTheTree(t *testing.T) {
	outside := t.TempDir()
	secret := write(t, outside, "environ", "PIFORGE_MASTER_KEY=deadbeef\n")

	root := t.TempDir()
	write(t, root, "leakskill/SKILL.md", "Innocent.\n")
	link(t, secret, filepath.Join(root, "leakskill", "key.txt"))

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = tree.ReadFile("leakskill/key.txt", 1<<20)
	assertRefusal(t, err, KindSymlink, "leakskill/key.txt")
	if strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("the refusal leaked the target: %v", err)
	}
}

// A link that does not resolve (a dangling one) is refused the same way: the
// rule is about the link, not about whether its target happens to exist.
func TestReadFileRefusesADanglingLink(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skill/SKILL.md", "x\n")
	link(t, filepath.Join(t.TempDir(), "does-not-exist"), filepath.Join(root, "skill", "key.txt"))

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = tree.ReadFile("skill/key.txt", 1024)
	assertRefusal(t, err, KindSymlink, "skill/key.txt")
}

// A link in a *parent* directory is the same primitive with one more level: the
// path looks ordinary, and every component still has to be a real directory.
func TestReadFileRefusesALinkInAParentDirectory(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "SKILL.md", "stolen\n")

	root := t.TempDir()
	link(t, outside, filepath.Join(root, "skills"))

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = tree.ReadFile("skills/SKILL.md", 1024)
	assertRefusal(t, err, KindSymlink, "skills")
}

// A directory is not a file: FindSkills would otherwise read a directory's
// bytes (or fail with an errno nobody can act on).
func TestReadFileRefusesADirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skill/SKILL.md", "x\n")
	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = tree.ReadFile("skill", 1024)
	assertRefusal(t, err, KindNotRegular, "skill")
}

func TestReadFileRefusesAnOversizedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "skill/big.txt", strings.Repeat("x", 2048))
	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = tree.ReadFile("skill/big.txt", 1024)
	assertRefusal(t, err, KindTooLarge, "skill/big.txt")
}

func TestReadFileRefusesATraversingOrAbsolutePath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x")
	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, rel := range []string{"../a.txt", "a/../../a.txt", "/etc/passwd", `\Windows\win.ini`, "a\x00b"} {
		if _, err := tree.ReadFile(rel, 1024); err == nil {
			t.Fatalf("ReadFile(%q) was allowed", rel)
		} else if !errors.As(err, new(*Refusal)) {
			t.Fatalf("ReadFile(%q) error %v is not a Refusal", rel, err)
		}
	}
}

// Walk is the fence: a link anywhere in the tree — including one in a directory
// no skill would ever read — refuses the whole traversal.
func TestWalkRefusesALinkAnywhereInTheTree(t *testing.T) {
	root := t.TempDir()
	write(t, root, "good/SKILL.md", "fine\n")
	write(t, root, "tools/readme.txt", "fine\n")
	link(t, filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(root, "tools", "link"))

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seen := 0
	err = tree.Walk("", func(rel string, info os.FileInfo) error {
		seen++
		return nil
	})
	assertRefusal(t, err, KindSymlink, "tools/link")
	if seen == 0 {
		t.Fatal("the walk visited nothing before refusing")
	}
}

func TestWalkSkipsGitStorage(t *testing.T) {
	root := t.TempDir()
	write(t, root, "SKILL.md", "fine\n")
	write(t, root, ".git/config", "[core]\n")
	// .git is skipped, so a link git itself might keep there is not the tenant's
	// content and does not refuse the walk.
	link(t, filepath.Join(t.TempDir(), "x"), filepath.Join(root, ".git", "HEAD"))

	tree, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var rels []string
	if werr := tree.WalkFiles("", func(rel string, info os.FileInfo) error {
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); werr != nil {
		t.Fatalf("WalkFiles: %v", werr)
	}
	if len(rels) != 1 || rels[0] != "SKILL.md" {
		t.Fatalf("visited %v, want only SKILL.md", rels)
	}
}

// On the platform this control plane actually runs on, the two paths from the
// report are the ones that matter.
func TestRefusesTheReportsOwnTargets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the report's targets are Linux paths")
	}
	cases := map[string]string{
		"proc": "/proc/1/environ",
		"data": "/data/master.key",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "leakskill/SKILL.md", "x\n")
			link(t, target, filepath.Join(root, "leakskill", "key.txt"))
			tree, err := Open(root)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := tree.ReadFile("leakskill/key.txt", 1<<20); err == nil {
				t.Fatalf("read %s through a link", target)
			}
		})
	}
}
