package skillsnap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A snapshot belongs to exactly one session, so its directory is named after the
// id: the janitor can then answer "is this garbage?" with one set lookup, and a
// delete does not have to re-derive a slug.
func TestNameIsTheSessionID(t *testing.T) {
	id := "ses_01M687KJ58DMG4B9VN27776T2J"
	if got := Name(id); got != id {
		t.Fatalf("Name(%q) = %q, want the id itself", id, got)
	}
	if got := Dir("/data/skill-snapshots", id); got != filepath.Join("/data/skill-snapshots", id) {
		t.Fatalf("Dir = %q", got)
	}
}

// A crafted id must not escape the snapshot root. Session ids never do; the
// fallback is the old slug rule, which folds every separator away.
func TestNameCannotEscapeTheRoot(t *testing.T) {
	got := Name("../../etc")
	if strings.ContainsAny(got, `/\`) || got == ".." || got == "." {
		t.Fatalf("Name = %q, the result escapes the snapshot root", got)
	}
	if got == "" {
		t.Fatal("Name returned an empty directory name")
	}
}

// Older builds named the directory after the id's slug (`ses-01m…`). A sweep
// must treat that as live too, or an upgrade would delete a running session's
// snapshot out from under its mount.
func TestNamesCoversTheLegacyLayout(t *testing.T) {
	id := "ses_01M687KJ58DMG4B9VN27776T2J"
	names := Names(id)
	if len(names) != 2 {
		t.Fatalf("Names = %v, want the current and the legacy name", names)
	}
	legacy := strings.ToLower(strings.ReplaceAll(id, "_", "-"))
	if names[1] != legacy {
		t.Fatalf("legacy name = %q, want %q", names[1], legacy)
	}
	if !IsSessionDir(names[0]) || !IsSessionDir(names[1]) {
		t.Fatalf("a live name is not recognised: %v", names)
	}
	live := LiveNames([]string{id})
	for _, name := range names {
		if !live[name] {
			t.Fatalf("%q is not live after LiveNames", name)
		}
	}
}

// The safety boundary: only directories shaped like a session snapshot are ours.
func TestIsSessionDirRejectsForeignNames(t *testing.T) {
	ours := []string{"ses_01JABC", "ses-01jabc"}
	foreign := []string{"notes", "usr_01JABC", "prj_01JABC", "ses_", "ses", ".", "..", "ses_../x"}
	for _, name := range ours {
		if !IsSessionDir(name) {
			t.Fatalf("%q should be a session snapshot directory", name)
		}
	}
	for _, name := range foreign {
		if IsSessionDir(name) {
			t.Fatalf("%q must not be treated as a session snapshot directory", name)
		}
	}
}

// A snapshot that is still a mount point cannot be removed. Every directory
// under it is detached first, and the removal only succeeds after that.
func TestRemoveDirDetachesBeforeRemoving(t *testing.T) {
	root := t.TempDir()
	id := "ses_01JABC"
	dir := Dir(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "review"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review", "SKILL.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var detached []string
	unmount := func(target string) error {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("detach %q after it was removed: %v", target, err)
		}
		detached = append(detached, target)
		return nil
	}
	freed, err := RemoveDir(root, id, unmount)
	if err != nil {
		t.Fatalf("RemoveDir: %v", err)
	}
	if len(detached) == 0 {
		t.Fatal("no directory was detached before the removal")
	}
	if detached[len(detached)-1] != dir {
		t.Fatalf("detached %v, want the snapshot root %q", detached, dir)
	}
	if freed.Dirs != 1 || freed.Bytes == 0 {
		t.Fatalf("freed = %+v, want one directory with bytes", freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the snapshot survived: %v", err)
	}
}

// A missing snapshot is not a failure: the janitor and the delete handler both
// run after a build that may never have created one.
func TestRemoveMissingSnapshotIsNotAnError(t *testing.T) {
	root := t.TempDir()
	freed, err := Remove(root, "ses_01JABC", nil)
	if err != nil {
		t.Fatalf("Remove of a missing snapshot: %v", err)
	}
	if freed.Dirs != 0 || freed.Bytes != 0 {
		t.Fatalf("freed = %+v, want nothing", freed)
	}
}

// A read-only copy left behind by a crashed run must still be removable: the
// same retry-with-writable-modes the sandbox destroy uses.
func TestRemoveDirClearsReadOnlyBits(t *testing.T) {
	root := t.TempDir()
	id := "ses_01JABC"
	dir := Dir(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "review"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "review", "SKILL.md")
	if err := os.WriteFile(file, []byte("x"), 0o444); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "review"), 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = chmodTree(dir, false) })
	if _, err := RemoveDir(root, id, nil); err != nil {
		t.Fatalf("RemoveDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the read-only snapshot survived: %v", err)
	}
}

// Remove is the delete path: it takes a session id, not a directory name, and
// clears both layouts so an upgrade cannot leave one behind.
func TestRemoveClearsBothLayouts(t *testing.T) {
	root := t.TempDir()
	id := "ses_01M687KJ58DMG4B9VN27776T2J"
	for _, name := range Names(id) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	freed, err := Remove(root, id, nil)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if freed.Dirs != 2 {
		t.Fatalf("freed = %+v, want both layouts", freed)
	}
	for _, name := range Names(id) {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", name, err)
		}
	}
}

// RemoveDir refuses a name it did not produce, so a bug in the caller cannot
// turn into "delete an arbitrary directory under the data volume".
func TestRemoveDirRefusesForeignNames(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "notes")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := RemoveDir(root, "notes", nil); err == nil {
		t.Fatal("RemoveDir accepted a foreign directory name")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("the foreign directory was removed: %v", err)
	}
}
