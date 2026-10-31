//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// bindReadOnlyForTest mounts source at target read-only, the way
// materialize's Linux implementation does. It lives here so the sandbox test
// does not depend on the materialize package.
func bindReadOnlyForTest(source, target string) error {
	if err := syscall.Mount(source, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return err
	}
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_REC)
	if err := syscall.Mount("", target, "", flags, ""); err != nil {
		_ = syscall.Unmount(target, syscall.MNT_DETACH)
		return err
	}
	return nil
}

// FR-14.2 puts read-only bind mounts under `.pi/skills`; a live mount makes its
// mount point busy, so deleting the sandbox (and with it the project) would fail
// with EBUSY. removeTreeWritable detaches them first; this test mounts a real
// directory, deletes the tree and asserts it is gone.
func TestDestroyDetachesMountedSkills(t *testing.T) {
	root := t.TempDir()
	mount := filepath.Join(root, ".pi", "skills", "review")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatalf("mkdir mount point: %v", err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Review\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := bindReadOnlyForTest(source, mount); err != nil {
		t.Skipf("this host cannot bind mount (expected without CAP_SYS_ADMIN): %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(mount, syscall.MNT_DETACH) })

	if err := removeTreeWritable(root); err != nil {
		t.Fatalf("removeTreeWritable: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the sandbox directory survived a mounted skill: %v", err)
	}
	// The source snapshot is not the thing being deleted.
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatalf("the mount source was deleted with the sandbox: %v", err)
	}
}

// A branch session has its own linked worktree inside the same sandbox, and its
// own `.pi/skills`. The detach has to reach those too: a project with a branch
// session used to stay undeletable (EBUSY) because only the primary checkout was
// unmounted.
func TestDestroyDetachesMountedSkillsInBranchWorktrees(t *testing.T) {
	root := t.TempDir()
	mounts := []string{
		filepath.Join(root, ".pi", "skills", "review"),
		filepath.Join(root, "worktrees", "branch", ".pi", "skills", "review"),
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Review\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	for _, mount := range mounts {
		if err := os.MkdirAll(mount, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", mount, err)
		}
		if err := bindReadOnlyForTest(source, mount); err != nil {
			t.Skipf("this host cannot bind mount (expected without CAP_SYS_ADMIN): %v", err)
		}
	}
	t.Cleanup(func() {
		for _, mount := range mounts {
			_ = syscall.Unmount(mount, syscall.MNT_DETACH)
		}
	})

	if err := removeTreeWritable(root); err != nil {
		t.Fatalf("removeTreeWritable: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the sandbox survived a branch worktree's mounted skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatalf("the mount source was deleted with the sandbox: %v", err)
	}
}
