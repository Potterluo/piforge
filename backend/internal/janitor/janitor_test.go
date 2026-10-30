package janitor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

type fakeIndex map[string]string

func (f fakeIndex) ProjectIndex(context.Context) (map[string]string, error) {
	return map[string]string(f), nil
}

// path builds a directory with one file in it, so a removal has something to
// remove rather than an empty entry.
func mkdir(t *testing.T, root, rel string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func testJanitor(t *testing.T, root string, index map[string]string) *Janitor {
	t.Helper()
	return New(Config{
		Root:     root,
		Projects: fakeIndex(index),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// A directory with no project row is an orphan; one with a row is a live
// sandbox and must survive, contents and all.
func TestSweepRemovesOnlyOrphans(t *testing.T) {
	root := t.TempDir()
	live := mkdir(t, root, "usr_A/prj_keep")
	orphan := mkdir(t, root, "usr_A/prj_gone")
	otherOwner := mkdir(t, root, "usr_B/prj_owned0001")

	j := testJanitor(t, root, map[string]string{
		"prj_keep":      "usr_A",
		"prj_owned0001": "usr_A",
	})
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedDirs != 2 {
		t.Fatalf("removed = %d, want 2 (the missing row and the wrong owner): %+v", res.RemovedDirs, res)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the live sandbox was removed: %v", err)
	}
	for _, dir := range []string{orphan, otherOwner} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after the sweep (err=%v)", dir, err)
		}
	}
}

// The provider keeps its state next to the sandboxes; an orphaned directory's
// state file goes with it, or a later Ensure re-adopts a project that is gone.
func TestSweepRemovesTheOrphansStateFile(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "usr_A/prj_gone")
	stateDir := filepath.Join(root, stateDirName, "usr_A")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(stateDir, "prj_gone.json")
	if err := os.WriteFile(state, []byte(`{"id":"sbx_x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := testJanitor(t, root, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedDirs != 1 || res.RemovedState != 1 {
		t.Fatalf("result = %+v, want one directory and one state file", res)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("the orphan's state file survived: %v", err)
	}
	// The _state tree itself is not a project and is left alone.
	if _, err := os.Stat(filepath.Join(root, stateDirName)); err != nil {
		t.Fatalf("the _state root was removed: %v", err)
	}
}

// Anything that does not look like the provider's own layout is somebody
// else's directory. A sweep that deletes those is worse than an orphan.
func TestSweepLeavesForeignDirectoriesAlone(t *testing.T) {
	root := t.TempDir()
	foreign := []string{
		"notes",              // no usr_ prefix
		"usr_A/scratch",      // no prj_ prefix
		"usr_A/prj_has-dash", // not an identifier
	}
	for _, rel := range foreign {
		mkdir(t, root, rel)
	}
	res, err := testJanitor(t, root, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedDirs != 0 {
		t.Fatalf("removed %d foreign directories, want 0", res.RemovedDirs)
	}
	for _, rel := range foreign {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s was removed: %v", rel, err)
		}
	}
}

// A missing root is a deployment that never created a sandbox. Not an error.
func TestSweepWithoutARootIsANoOp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	res, err := testJanitor(t, root, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedDirs != 0 {
		t.Fatalf("removed %d directories from a missing root", res.RemovedDirs)
	}
}

func TestDryRunRemovesNothing(t *testing.T) {
	root := t.TempDir()
	orphan := mkdir(t, root, "usr_A/prj_gone")
	j := testJanitor(t, root, nil)
	j.cfg.DryRun = true
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedDirs != 1 {
		t.Fatalf("dry run reported %d, want 1", res.RemovedDirs)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("a dry run removed the directory: %v", err)
	}
}
