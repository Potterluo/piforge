package materialize

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-14.2: the mechanism is a platform decision, not a comment. Linux gets a
// bind mount; every other platform degrades to a read-only per-skill copy and
// says so.
func TestPlatformMechanism(t *testing.T) {
	cases := map[string]Mechanism{
		"linux":   MechanismBindRO,
		"windows": MechanismCopyRO,
		"darwin":  MechanismCopyRO,
		"freebsd": MechanismCopyRO,
		"":        MechanismCopyRO,
	}
	for goos, want := range cases {
		if got := PlatformMechanism(goos); got != want {
			t.Errorf("PlatformMechanism(%q) = %q, want %q", goos, got, want)
		}
	}
}

// FR-14.2 on Windows: each skill is a read-only copy, never a pointer at the
// bundle. The agent cannot overwrite it, editing the bundle does not reach a
// running session, and a new session gets the new content.
func TestMountCopiesReadOnlyOffTheSourcePath(t *testing.T) {
	bundles := t.TempDir()
	source := writeBundle(t, bundles, "review-1", "---\nname: review\n---\nRead the diff.\n")

	root := filepath.Join(t.TempDir(), "repo")
	t.Cleanup(func() { _ = chmodTree(root, false) })

	row := &contracts.Skill{ID: "skl_1", Name: "review", BundleKey: "review-1", Version: "1", Enabled: true}
	result, err := Mount(root, []*contracts.Skill{row}, nil, Options{SkillsDir: bundles, Platform: "windows"})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(result.Mounted) != 1 {
		t.Fatalf("mounted = %+v", result.Mounted)
	}
	mounted := result.Mounted[0]
	if mounted.Mechanism != string(MechanismCopyRO) {
		t.Fatalf("mechanism = %q, want copy-ro on windows", mounted.Mechanism)
	}
	if filepath.Clean(mounted.Dir) == filepath.Clean(source) {
		t.Fatal("the mounted path is the source path")
	}
	skillFile := filepath.Join(mounted.Dir, "SKILL.md")

	// A write through the mounted path is refused.
	if err := os.WriteFile(skillFile, []byte("agent was here\n"), 0o644); err == nil {
		t.Fatal("a skill copied for the agent was writable")
	}
	if body, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("the failed write changed the mounted copy: %q, %v", body, err)
	}
	// The source bundle is untouched by the attempt as well.
	if body, err := os.ReadFile(filepath.Join(source, "SKILL.md")); err != nil || !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("source bundle = %q, %v", body, err)
	}

	// Editing the bundle does not retroactively change the running session's copy.
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"),
		[]byte("---\nname: review\n---\nRead every hunk twice.\n"), 0o644); err != nil {
		t.Fatalf("edit bundle: %v", err)
	}
	if _, err := Mount(root, []*contracts.Skill{row}, nil, Options{SkillsDir: bundles, Platform: "windows"}); err != nil {
		t.Fatalf("re-mount after edit: %v", err)
	}
	if body, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("an edit reached the running session's mount: %q, %v", body, err)
	}

	// A new session (a new worktree) picks up the edited content.
	newRoot := filepath.Join(t.TempDir(), "repo")
	t.Cleanup(func() { _ = chmodTree(newRoot, false) })
	fresh, err := Mount(newRoot, []*contracts.Skill{row}, nil, Options{SkillsDir: bundles, Platform: "windows"})
	if err != nil {
		t.Fatalf("Mount new session: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(fresh.Mounted[0].Dir, "SKILL.md")); err != nil || !strings.Contains(string(body), "twice") {
		t.Fatalf("a new session did not get the edited skill: %q, %v", body, err)
	}
}

// FR-14.2 on a host that can bind: the mount source is a frozen snapshot, not
// the live bundle. That is what lets a bind mount be both read-only and stable.
func TestMountBindsAFrozenSnapshotNotTheBundle(t *testing.T) {
	bundles := t.TempDir()
	source := writeBundle(t, bundles, "review-1", "---\nname: review\n---\nVersion one.\n")
	snapshots := filepath.Join(t.TempDir(), "snapshots")

	root := filepath.Join(t.TempDir(), "repo")
	var calls [][2]string
	opts := Options{
		SkillsDir:   bundles,
		SnapshotDir: snapshots,
		Platform:    "linux",
		BindMount: func(src, dst string) error {
			calls = append(calls, [2]string{src, dst})
			// A real bind makes the snapshot's files visible at dst; the fake
			// reproduces that so the mount loop's SKILL.md check sees the truth.
			return copyTree(src, dst)
		},
		Unmount: func(string) error { return nil },
	}
	row := &contracts.Skill{ID: "skl_1", Name: "review", BundleKey: "review-1", Enabled: true}

	result, err := Mount(root, []*contracts.Skill{row}, nil, opts)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if got := result.Mounted[0].Mechanism; got != string(MechanismBindRO) {
		t.Fatalf("mechanism = %q, want bind-ro", got)
	}
	if len(calls) != 1 {
		t.Fatalf("bind calls = %d, want 1", len(calls))
	}
	snapshot := calls[0][0]
	if filepath.Clean(snapshot) == filepath.Clean(source) {
		t.Fatal("the bind source is the live bundle; an edit would reach a running session")
	}
	if !strings.HasPrefix(filepath.Clean(snapshot), filepath.Clean(snapshots)) {
		t.Fatalf("snapshot %q is outside the snapshot root %q", snapshot, snapshots)
	}
	if body, err := os.ReadFile(filepath.Join(snapshot, "SKILL.md")); err != nil || !strings.Contains(string(body), "Version one") {
		t.Fatalf("snapshot content = %q, %v", body, err)
	}
	if calls[0][1] != result.Mounted[0].Dir {
		t.Fatalf("bind target = %q, mounted dir = %q", calls[0][1], result.Mounted[0].Dir)
	}

	// A later pass for the same session keeps the mount: it does not re-bind and
	// the snapshot is not refreshed from the edited bundle.
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("Version two.\n"), 0o644); err != nil {
		t.Fatalf("edit bundle: %v", err)
	}
	if _, err := Mount(root, []*contracts.Skill{row}, nil, opts); err != nil {
		t.Fatalf("re-mount: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("a later pass re-mounted: %d calls", len(calls))
	}
	if body, err := os.ReadFile(filepath.Join(snapshot, "SKILL.md")); err != nil || !strings.Contains(string(body), "Version one") {
		t.Fatalf("the frozen snapshot changed: %q, %v", body, err)
	}
}

// A container without CAP_SYS_ADMIN cannot mount. The degradation must be named,
// not silent, and it must still produce a read-only tree.
func TestMountDegradesWhenBindIsRefused(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "review-1", "Review\n")
	root := filepath.Join(t.TempDir(), "repo")
	t.Cleanup(func() { _ = chmodTree(root, false) })

	result, err := Mount(root, []*contracts.Skill{
		{ID: "skl_1", Name: "review", BundleKey: "review-1", Enabled: true},
	}, nil, Options{
		SkillsDir:   bundles,
		SnapshotDir: filepath.Join(t.TempDir(), "snapshots"),
		Platform:    "linux",
		BindMount:   func(string, string) error { return os.ErrPermission },
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if got := result.Mounted[0].Mechanism; got != string(MechanismCopyRO) {
		t.Fatalf("mechanism after a refused bind = %q, want copy-ro", got)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "bind mount refused") {
		t.Fatalf("warnings = %v, want the refusal explained", result.Warnings)
	}
	skillFile := filepath.Join(result.Mounted[0].Dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("nope\n"), 0o644); err == nil {
		t.Fatal("the fallback copy is writable")
	}
}

// A stale mount left by a crashed session must not stop the next provisioning
// pass. The kernel detach is best-effort and the read-only bits are cleared
// before removal.
func TestStaleMountDoesNotBreakProvisioning(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "review-1", "Fresh\n")
	root := filepath.Join(t.TempDir(), "repo")
	t.Cleanup(func() { _ = chmodTree(root, false) })

	target := filepath.Join(root, ".pi", "skills", "review")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir stale target: %v", err)
	}
	stale := filepath.Join(target, "leftover.txt")
	if err := os.WriteFile(stale, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	// A stale mount copies the symptom that used to wedge provisioning: the
	// directory is not removable and the files inside refuse writes.
	if err := chmodTree(target, true); err != nil {
		t.Fatalf("make stale target read-only: %v", err)
	}

	result, err := Mount(root, []*contracts.Skill{
		{ID: "skl_1", Name: "review", BundleKey: "review-1", Enabled: true},
	}, nil, Options{
		SkillsDir: bundles,
		Platform:  "windows",
		Unmount:   func(string) error { return os.ErrPermission },
	})
	if err != nil {
		t.Fatalf("Mount over a stale mount: %v", err)
	}
	if got := filepath.Base(result.Mounted[0].Dir); got != "review" {
		t.Fatalf("mounted dir = %q", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale file survived: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(result.Mounted[0].Dir, "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "Fresh") {
		t.Fatalf("mounted SKILL.md = %q, %v", body, err)
	}
}

// The read-only promise is probed, not assumed: root on Linux ignores 0444, and
// a mechanism that claimed copy-ro there would be a lie the agent can disprove
// with one write.
func TestMountedFileIsWritableProbe(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !mountedFileIsWritable(dir) {
		t.Fatal("a 0644 file was reported read-only")
	}
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	got := mountedFileIsWritable(dir)
	switch runtime.GOOS {
	case "windows":
		if got {
			t.Fatal("a read-only file was reported writable on windows")
		}
	default:
		if os.Geteuid() == 0 {
			if !got {
				t.Fatal("root could not open a 0444 file; the probe under-reports the bypass")
			}
		} else if got {
			t.Fatal("a non-root process opened a 0444 file for writing")
		}
	}
}
