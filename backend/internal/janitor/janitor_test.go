package janitor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/skillsnap"
)

type fakeIndex map[string]string

func (f fakeIndex) ProjectIndex(context.Context) (map[string]string, error) {
	return map[string]string(f), nil
}

type fakeSessions []string

func (f fakeSessions) SessionIDs(context.Context) ([]string, error) {
	return []string(f), nil
}

type failingSessions struct{}

func (failingSessions) SessionIDs(context.Context) ([]string, error) {
	return nil, errors.New("database is away")
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

// snapshotJanitor wires only the snapshot half: no sandbox root, one live
// session id list.
func snapshotJanitor(t *testing.T, root string, live []string) *Janitor {
	t.Helper()
	return New(Config{
		SnapshotRoot: root,
		Sessions:     fakeSessions(live),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
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

// ---------------------------------------------------------------------------
// FR-14.2: skill snapshots
// ---------------------------------------------------------------------------

// A snapshot directory exists for one session; when the session row is gone the
// directory is an orphan. A live session's snapshot — including one named the
// way older builds named it — must survive.
func TestSweepRemovesOnlySnapshotsWithoutASession(t *testing.T) {
	root := t.TempDir()
	liveID := "ses_01MLIVE00000000000000000A"
	oldID := "ses_01MOLD000000000000000000B"
	goneID := "ses_01MGONE000000000000000000"
	for _, name := range []string{liveID, skillsnap.Name(goneID), strings.ToLower(strings.ReplaceAll(oldID, "_", "-"))} {
		mkdir(t, root, name)
	}

	res, err := snapshotJanitor(t, root, []string{liveID, oldID}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 1 {
		t.Fatalf("removed %d snapshots, want 1: %+v", res.RemovedSnapshots, res)
	}
	if res.SnapshotBytes == 0 {
		t.Fatalf("freed bytes were not reported: %+v", res)
	}
	for _, kept := range []string{liveID, strings.ToLower(strings.ReplaceAll(oldID, "_", "-"))} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Fatalf("a live session's snapshot was removed (%s): %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, skillsnap.Name(goneID))); !os.IsNotExist(err) {
		t.Fatalf("the orphaned snapshot survived: %v", err)
	}
}

// The snapshot is detached before it is removed: a live mount makes its
// directory busy, and a detach failure must be a retry, not an abort.
func TestSweepDetachesAMountedSnapshotFirst(t *testing.T) {
	root := t.TempDir()
	id := "ses_01MGONE000000000000000000"
	dir := mkdir(t, root, id)
	_ = mkdir(t, dir, filepath.Join("review", "scripts"))

	var detached []string
	j := snapshotJanitor(t, root, nil)
	j.cfg.Unmount = func(target string) error {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("detached %q after removal: %v", target, err)
		}
		detached = append(detached, target)
		return nil
	}
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 1 {
		t.Fatalf("result = %+v, want one snapshot", res)
	}
	if len(detached) < 3 {
		t.Fatalf("detached %v, want the snapshot and its skill directories", detached)
	}
	if detached[0] != filepath.Join(dir, "review", "scripts") && detached[len(detached)-1] != dir {
		t.Fatalf("detach order = %v, want deepest first and the snapshot last", detached)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the mounted snapshot survived: %v", err)
	}
}

// One snapshot that cannot be removed is logged and retried on the next pass;
// the rest of the sweep still runs.
func TestSweepKeepsGoingWhenOneSnapshotCannotBeRemoved(t *testing.T) {
	root := t.TempDir()
	stuck := "ses_01MSTUCK00000000000000000"
	gone := "ses_01MGONE000000000000000000"
	mkdir(t, root, stuck)
	mkdir(t, root, gone)

	attempts := 0
	j := snapshotJanitor(t, root, nil)
	j.cfg.RemoveSnapshot = func(root, name string, unmount func(string) error) (skillsnap.Freed, error) {
		attempts++
		if name == stuck {
			return skillsnap.Freed{}, errors.New("still busy")
		}
		return skillsnap.RemoveDir(root, name, unmount)
	}
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want both directories tried", attempts)
	}
	if res.Failures != 1 || res.RemovedSnapshots != 1 {
		t.Fatalf("result = %+v, want one failure and one removal", res)
	}
	if _, err := os.Stat(filepath.Join(root, stuck)); err != nil {
		t.Fatalf("the stuck snapshot was removed anyway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
		t.Fatalf("one failure aborted the sweep: %v", err)
	}

	// The retry: the same criterion (no session) is re-evaluated from scratch.
	j.cfg.RemoveSnapshot = skillsnap.RemoveDir
	res, err = j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 1 {
		t.Fatalf("the retry did not remove the snapshot: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, stuck)); !os.IsNotExist(err) {
		t.Fatalf("the stuck snapshot survived the retry: %v", err)
	}
}

// A store that cannot answer must abort the snapshot half: "no live set" is not
// "every snapshot is garbage".
func TestSweepRefusesToGuessWhenTheSessionIndexFails(t *testing.T) {
	root := t.TempDir()
	id := "ses_01MLIVE00000000000000000A"
	mkdir(t, root, id)

	j := New(Config{
		SnapshotRoot: root,
		Sessions:     failingSessions{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce reported success without a live-session index")
	}
	if _, err := os.Stat(filepath.Join(root, id)); err != nil {
		t.Fatalf("a snapshot was removed without knowing whether its session exists: %v", err)
	}
}

// Foreign directories under the snapshot root are not ours, whatever they hold.
func TestSweepLeavesForeignSnapshotDirectoriesAlone(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "notes")
	mkdir(t, root, "usr_01JABC")
	res, err := snapshotJanitor(t, root, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 0 {
		t.Fatalf("removed %d foreign directories", res.RemovedSnapshots)
	}
	for _, name := range []string{"notes", "usr_01JABC"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}

// The log line says how much was reclaimed, in count and bytes: an operator
// should not have to infer the sweep from a directory listing.
func TestSweepLogsTheCountAndBytesFreed(t *testing.T) {
	root := t.TempDir()
	id := "ses_01MGONE000000000000000000"
	dir := mkdir(t, root, id)
	body := strings.Repeat("x", 4096)
	if err := os.WriteFile(filepath.Join(dir, "payload.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var log bytes.Buffer
	j := New(Config{
		SnapshotRoot: root,
		Sessions:     fakeSessions(nil),
		Logger:       slog.New(slog.NewTextHandler(&log, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	line := log.String()
	if !strings.Contains(line, "removed orphaned skill snapshots") {
		t.Fatalf("no snapshot log line: %s", line)
	}
	if !strings.Contains(line, "count=1") || !strings.Contains(line, "bytes=") {
		t.Fatalf("the log line does not report count and bytes: %s", line)
	}
}

func TestDryRunReportsSnapshotsWithoutRemovingThem(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "ses_01MGONE000000000000000000")
	j := snapshotJanitor(t, root, nil)
	j.cfg.DryRun = true
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 1 {
		t.Fatalf("dry run reported %d, want 1", res.RemovedSnapshots)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a dry run removed the snapshot: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The whole runtime-directory inventory: pi-agents, artifacts, snapshots,
// skills — not only the frozen skill trees.
// ---------------------------------------------------------------------------

type fakeSandboxes []string

func (f fakeSandboxes) SandboxIDs(context.Context) ([]string, error) { return []string(f), nil }

type fakeSkills []string

func (f fakeSkills) SkillIDs(context.Context) ([]string, error) { return []string(f), nil }

type failingSandboxes struct{}

func (failingSandboxes) SandboxIDs(context.Context) ([]string, error) {
	return nil, errors.New("database is away")
}

type failingSkills struct{}

func (failingSkills) SkillIDs(context.Context) ([]string, error) {
	return nil, errors.New("database is away")
}

// inventoryJanitor wires every owned root in one pass, with the live sets the
// test wants.
func inventoryJanitor(t *testing.T, roots map[string]string, liveSessions, liveSandboxes, liveSkills []string) *Janitor {
	t.Helper()
	return New(Config{
		Root:          roots["sandboxes"],
		Projects:      fakeIndex(nil),
		SnapshotRoot:  roots["skill-snapshots"],
		AgentRoot:     roots["pi-agents"],
		ArtifactsRoot: roots["artifacts"],
		SnapshotsRoot: roots["snapshots"],
		SkillsRoot:    roots["skills"],
		Sessions:      fakeSessions(liveSessions),
		Sandboxes:     fakeSandboxes(liveSandboxes),
		Skills:        fakeSkills(liveSkills),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func newRoots(t *testing.T, names ...string) map[string]string {
	t.Helper()
	base := t.TempDir()
	roots := map[string]string{}
	for _, name := range names {
		roots[name] = filepath.Join(base, name)
	}
	return roots
}

// One orphan in every root is reclaimed in one pass, and a directory whose row
// still exists survives — that is the conservative half of the contract, tested
// per directory type rather than only for snapshots.
func TestSweepReclaimsEveryDirectoryTypeAndKeepsLiveOwners(t *testing.T) {
	roots := newRoots(t, "sandboxes", "skill-snapshots", "pi-agents", "artifacts", "snapshots", "skills")
	liveSession := "ses_01MLIVE00000000000000000A"
	goneSession := "ses_01MGONE000000000000000000"
	liveSandbox := "sbx_01MLIVE00000000000000000A"
	goneSandbox := "sbx_01MGONE000000000000000000"
	liveSkill := "skl_01MLIVE00000000000000000A"
	goneSkill := "skl_01MGONE000000000000000000"

	orphans := []string{}
	for _, dir := range []string{"skill-snapshots", "pi-agents", "artifacts"} {
		orphans = append(orphans, mkdir(t, roots[dir], goneSession))
		mkdir(t, roots[dir], liveSession)
	}
	orphans = append(orphans, mkdir(t, roots["snapshots"], goneSandbox))
	mkdir(t, roots["snapshots"], liveSandbox)
	orphans = append(orphans, mkdir(t, roots["skills"], goneSkill))
	orphans = append(orphans, mkdir(t, roots["skills"], goneSkill+".tmp"))
	mkdir(t, roots["skills"], liveSkill)
	mkdir(t, roots["skills"], liveSkill+".tmp")
	// A foreign directory in each root must survive even though it has no row.
	for _, dir := range []string{"skill-snapshots", "pi-agents", "artifacts", "snapshots", "skills"} {
		mkdir(t, roots[dir], "notes")
	}
	// The sandbox root needs the provider layout to be recognised.
	orphans = append(orphans, mkdir(t, roots["sandboxes"], "usr_A/prj_GONE0000000000000000000"))

	j := inventoryJanitor(t, roots, []string{liveSession}, []string{liveSandbox}, []string{liveSkill})
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.RemovedSnapshots != 1 || res.RemovedAgents != 1 || res.RemovedArtifacts != 1 ||
		res.RemovedSandboxShots != 1 || res.RemovedSkillBundles != 2 {
		t.Fatalf("result = %+v, want one of each session/sandbox type and two skill bundles (orphan + staging)", res)
	}
	if res.RemovedDirs != 1 {
		t.Fatalf("removed dirs = %d, want the sandbox orphan too: %+v", res.RemovedDirs, res)
	}
	if res.AgentBytes == 0 || res.ArtifactBytes == 0 || res.SandboxSnapshotBytes == 0 || res.SkillBytes == 0 {
		t.Fatalf("bytes were not measured per directory type: %+v", res)
	}
	for _, dir := range orphans {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("orphan %s survived the sweep (err=%v)", dir, err)
		}
	}
	live := []string{
		filepath.Join(roots["skill-snapshots"], liveSession),
		filepath.Join(roots["pi-agents"], liveSession),
		filepath.Join(roots["artifacts"], liveSession),
		filepath.Join(roots["snapshots"], liveSandbox),
		filepath.Join(roots["skills"], liveSkill),
		filepath.Join(roots["skills"], liveSkill+".tmp"),
	}
	for _, dir := range live {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("a live owner's directory was removed (%s): %v", dir, err)
		}
	}
	for _, dir := range []string{"skill-snapshots", "pi-agents", "artifacts", "snapshots", "skills"} {
		if _, err := os.Stat(filepath.Join(roots[dir], "notes")); err != nil {
			t.Fatalf("a foreign directory in %s was removed: %v", dir, err)
		}
	}
}

// One stuck directory must not stop the others or the next pass, for every new
// root type — the same guarantee the snapshot sweep already had.
func TestSweepKeepsGoingWhenANewDirectoryTypeCannotBeRemoved(t *testing.T) {
	roots := newRoots(t, "pi-agents", "artifacts", "snapshots", "skills")
	stuck := "ses_01MSTUCK00000000000000000"
	gone := "ses_01MGONE000000000000000000"
	mkdir(t, roots["pi-agents"], stuck)
	mkdir(t, roots["pi-agents"], gone)
	mkdir(t, roots["artifacts"], gone)
	mkdir(t, roots["snapshots"], "sbx_01MGONE000000000000000000")
	mkdir(t, roots["skills"], "skl_01MGONE000000000000000000")

	attempts := 0
	j := New(Config{
		AgentRoot:     roots["pi-agents"],
		ArtifactsRoot: roots["artifacts"],
		SnapshotsRoot: roots["snapshots"],
		SkillsRoot:    roots["skills"],
		Sessions:      fakeSessions(nil),
		Sandboxes:     fakeSandboxes(nil),
		Skills:        fakeSkills(nil),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	j.cfg.RemoveTree = func(root, name string, unmount func(string) error) (skillsnap.Freed, error) {
		attempts++
		if name == stuck {
			return skillsnap.Freed{}, errors.New("still busy")
		}
		return skillsnap.RemoveNamed(root, name, unmount)
	}
	res, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if attempts != 5 {
		t.Fatalf("attempts = %d, want every directory tried (2 agents + 1 artifact + 1 snapshot + 1 skill)", attempts)
	}
	if res.Failures != 1 {
		t.Fatalf("failures = %d, want the one stuck agent directory", res.Failures)
	}
	if res.RemovedAgents != 1 || res.RemovedArtifacts != 1 || res.RemovedSandboxShots != 1 || res.RemovedSkillBundles != 1 {
		t.Fatalf("one failure aborted other roots: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(roots["pi-agents"], stuck)); err != nil {
		t.Fatalf("the stuck directory was removed anyway: %v", err)
	}

	j.cfg.RemoveTree = skillsnap.RemoveNamed
	res, err = j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if res.RemovedAgents != 1 {
		t.Fatalf("the retry did not remove the stuck agent directory: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(roots["pi-agents"], stuck)); !os.IsNotExist(err) {
		t.Fatalf("the stuck agent directory survived the retry: %v", err)
	}
}

// A store that cannot answer aborts that root: "no live set" is not "everything
// is garbage", for each new index too.
func TestSweepRefusesToGuessWhenANewIndexFails(t *testing.T) {
	roots := newRoots(t, "pi-agents", "artifacts", "snapshots", "skills")
	agent := mkdir(t, roots["pi-agents"], "ses_01MLIVE00000000000000000A")
	artifact := mkdir(t, roots["artifacts"], "ses_01MLIVE00000000000000000A")
	snapshot := mkdir(t, roots["snapshots"], "sbx_01MLIVE00000000000000000A")
	skill := mkdir(t, roots["skills"], "skl_01MLIVE00000000000000000A")

	// Session index failure: both session-keyed roots must be untouched.
	j := New(Config{
		AgentRoot:     roots["pi-agents"],
		ArtifactsRoot: roots["artifacts"],
		Sessions:      failingSessions{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce reported success without a live-session index")
	}
	for _, dir := range []string{agent, artifact} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("a session directory was removed without knowing whether its session exists: %v", err)
		}
	}

	// Sandbox index failure: the snapshot root must be untouched.
	j = New(Config{
		SnapshotsRoot: roots["snapshots"],
		Sandboxes:     failingSandboxes{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce reported success without a live-sandbox index")
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("a sandbox snapshot was removed without knowing whether its sandbox exists: %v", err)
	}

	// Skill index failure: the bundle root must be untouched.
	j = New(Config{
		SkillsRoot: roots["skills"],
		Skills:     failingSkills{},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce reported success without a live-skill index")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("a skill bundle was removed without knowing whether its skill exists: %v", err)
	}
}

// The log line for each new root reports count and bytes, so growth and
// reclamation are visible from the deployment's logs alone.
func TestSweepLogsCountAndBytesForEveryNewRoot(t *testing.T) {
	roots := newRoots(t, "pi-agents", "artifacts", "snapshots", "skills")
	for _, pair := range []struct{ root, name string }{
		{"pi-agents", "ses_01MGONE000000000000000000"},
		{"artifacts", "ses_01MGONE000000000000000001"},
		{"snapshots", "sbx_01MGONE000000000000000000"},
		{"skills", "skl_01MGONE000000000000000000"},
	} {
		dir := mkdir(t, roots[pair.root], pair.name)
		if err := os.WriteFile(filepath.Join(dir, "payload.txt"), bytes.Repeat([]byte("x"), 64), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var log bytes.Buffer
	j := New(Config{
		AgentRoot:     roots["pi-agents"],
		ArtifactsRoot: roots["artifacts"],
		SnapshotsRoot: roots["snapshots"],
		SkillsRoot:    roots["skills"],
		Sessions:      fakeSessions(nil),
		Sandboxes:     fakeSandboxes(nil),
		Skills:        fakeSkills(nil),
		Logger:        slog.New(slog.NewTextHandler(&log, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	line := log.String()
	for _, want := range []string{
		"removed orphaned agent directories", "removed orphaned artifact directories",
		"removed orphaned sandbox snapshots", "removed orphaned skill bundles",
		"count=1", "bytes=",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log is missing %q:\n%s", want, line)
		}
	}
}

// fakeRetention records the cutoff it was asked for and answers with counts.
type fakeRetention struct {
	cutoff time.Time
	rows   int64
	runs   int64
	err    error
	calls  int
}

func (f *fakeRetention) PruneAuditBefore(_ context.Context, cutoff time.Time) (int64, int64, error) {
	f.calls++
	f.cutoff = cutoff
	return f.rows, f.runs, f.err
}

// Retention is a policy pass: it runs when a positive day count is configured,
// asks for exactly that cutoff, and reports the count. Days=0 keeps the trail
// forever and must not call the store at all.
func TestAuditRetentionRunsOnlyWithAPositivePolicy(t *testing.T) {
	retention := &fakeRetention{rows: 7, runs: 3}
	j := New(Config{
		AuditRetention:     retention,
		AuditRetentionDays: 30,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	before := time.Now()
	result, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if retention.calls != 1 {
		t.Fatalf("retention was called %d times, want 1", retention.calls)
	}
	wantCutoff := before.Add(-30 * 24 * time.Hour)
	if delta := retention.cutoff.Sub(wantCutoff); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("cutoff = %s, want about %s", retention.cutoff, wantCutoff)
	}
	if result.RemovedAuditRows != 7 || result.RemovedAuditRuns != 3 {
		t.Fatalf("result = %+v", result)
	}

	// Zero days means "keep forever": no call, and the pass is not enabled.
	off := &fakeRetention{}
	j = New(Config{
		AuditRetention:     off,
		AuditRetentionDays: 0,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce(off): %v", err)
	}
	if off.calls != 0 {
		t.Fatalf("retention ran with days=0 (%d calls)", off.calls)
	}

	// A store failure is logged and counted, not turned into a panic or a
	// half-applied sweep.
	broken := &fakeRetention{err: errors.New("database is away")}
	j = New(Config{
		AuditRetention:     broken,
		AuditRetentionDays: 1,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	result, err = j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(broken) returned an error: %v", err)
	}
	if result.Failures != 1 {
		t.Fatalf("failures = %d, want 1", result.Failures)
	}
}
