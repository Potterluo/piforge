package materialize

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-14.2: the mechanism is a platform-and-capability decision, not a comment.
//
// The order matters: a platform without bind mounts is copy-ro whatever the
// capability set says, Linux with CAP_SYS_ADMIN is bind-ro, and Linux without it
// is copy-ro — never the writable `copy`, which only a failed read-only probe
// may produce. A read-only copy needs no capability; that is what makes the
// default deployment correct without SYS_ADMIN.
func TestMountPlanDecisionOrder(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		caps     Capabilities
		want     Mechanism
		canBind  bool
	}{
		{name: "linux with CAP_SYS_ADMIN", goos: "linux", caps: Capabilities{BindRO: true}, want: MechanismBindRO, canBind: true},
		{name: "linux without CAP_SYS_ADMIN", goos: "linux", caps: Capabilities{}, want: MechanismCopyRO, canBind: false},
		{name: "windows never binds", goos: "windows", caps: Capabilities{BindRO: true}, want: MechanismCopyRO, canBind: false},
		{name: "darwin never binds", goos: "darwin", caps: Capabilities{BindRO: true}, want: MechanismCopyRO, canBind: false},
		{name: "freebsd never binds", goos: "freebsd", caps: Capabilities{}, want: MechanismCopyRO, canBind: false},
		{name: "unknown platform", goos: "", caps: Capabilities{BindRO: true}, want: MechanismCopyRO, canBind: false},
	}
	for _, tc := range cases {
		plan := PlanFor(tc.goos, tc.caps)
		if plan.Preferred != tc.want || plan.CanBind != tc.canBind {
			t.Errorf("%s: PlanFor = %+v, want mechanism %q canBind %v",
				tc.name, plan, tc.want, tc.canBind)
		}
	}
}

// The override keeps the decision testable on any machine: the same binary that
// runs on Windows can assert what Linux would do.
func TestPlatformPlanOverride(t *testing.T) {
	if got := PlatformPlan("linux", Capabilities{BindRO: true}); got.Preferred != MechanismBindRO {
		t.Fatalf("override plan = %+v, want bind-ro", got)
	}
	if got := PlatformPlan("linux", Capabilities{}); got.Preferred != MechanismCopyRO || got.CanBind {
		t.Fatalf("no-capability override plan = %+v, want copy-ro without a bind attempt", got)
	}
}

// modeBitsBind asks the host the only question that decides whether copy-ro is
// an honest label: does a 0444 file really refuse a write for this process?
//
// It is asked rather than assumed because the answer depends on the platform
// *and* on the uid. Windows maps the bit to FILE_ATTRIBUTE_READONLY, which binds
// everyone; Linux binds the owner, but a root process carrying CAP_DAC_OVERRIDE
// ignores it — which is `go test ./...` on a stock Linux root host, and the
// deployment shape a Linux operator is most likely to run it in. Tests that
// hard-coded copy-ro were only ever right on Windows and on non-root Linux, and
// this pass measured them failing on Linux root (docs/LINUX-VERIFY.md, row L9).
func modeBitsBind(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("probe\n"), 0o644); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	if err := chmodTree(dir, true); err != nil {
		t.Fatalf("probe chmod: %v", err)
	}
	t.Cleanup(func() { _ = chmodTree(dir, false) })
	return !mountedFileIsWritable(dir)
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
	binds := modeBitsBind(t)
	want := MechanismCopyRO
	if !binds {
		want = MechanismCopy
	}
	if mounted.Mechanism != string(want) {
		t.Fatalf("mechanism = %q, want %q (the read-only bits bind this process: %v)", mounted.Mechanism, want, binds)
	}
	if filepath.Clean(mounted.Dir) == filepath.Clean(source) {
		t.Fatal("the mounted path is the source path")
	}
	skillFile := filepath.Join(mounted.Dir, "SKILL.md")

	// A write through the mounted path is refused — when the mode bits bind this
	// process. Where they do not (root with CAP_DAC_OVERRIDE), the copy is still
	// frozen onto a path the bundle does not own, which is the half of the
	// guarantee that does not depend on a capability, and the mechanism says
	// `copy` instead of claiming `copy-ro`.
	if binds {
		if err := os.WriteFile(skillFile, []byte("agent was here\n"), 0o644); err == nil {
			t.Fatal("a skill copied for the agent was writable")
		}
		if body, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(body), "Read the diff") {
			t.Fatalf("the failed write changed the mounted copy: %q, %v", body, err)
		}
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
		// A host with the capability: the bind path is the one under test.
		Capabilities: &Capabilities{BindRO: true},
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
		SkillsDir:    bundles,
		SnapshotDir:  filepath.Join(t.TempDir(), "snapshots"),
		Platform:     "linux",
		Capabilities: &Capabilities{BindRO: true},
		BindMount:    func(string, string) error { return os.ErrPermission },
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	want := MechanismCopyRO
	binds := modeBitsBind(t)
	if !binds {
		want = MechanismCopy
	}
	if got := result.Mounted[0].Mechanism; got != string(want) {
		t.Fatalf("mechanism after a refused bind = %q, want %q (the read-only bits bind this process: %v)", got, want, binds)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "bind mount refused") {
		t.Fatalf("warnings = %v, want the refusal explained", result.Warnings)
	}
	if binds {
		skillFile := filepath.Join(result.Mounted[0].Dir, "SKILL.md")
		if err := os.WriteFile(skillFile, []byte("nope\n"), 0o644); err == nil {
			t.Fatal("the fallback copy is writable")
		}
	}
}

// The default deployment: no CAP_SYS_ADMIN, so no bind is attempted and no
// snapshot is created (there is nothing to share), and the mechanism is copy-ro
// rather than the writable `copy`. The read-only bits have to hold for this
// process; a root process that still carries CAP_DAC_OVERRIDE is the one case
// where the probe may only report `copy`, and the test follows the probe rather
// than the platform so it stays honest on such a host.
func TestNoCapabilityIsAReadOnlyCopyWithoutASnapshot(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "review-1", "Review\n")
	root := filepath.Join(t.TempDir(), "repo")
	t.Cleanup(func() { _ = chmodTree(root, false) })
	snapshots := filepath.Join(t.TempDir(), "snapshots")

	binds := 0
	result, err := Mount(root, []*contracts.Skill{
		{ID: "skl_1", Name: "review", BundleKey: "review-1", Enabled: true},
	}, nil, Options{
		SkillsDir:    bundles,
		SnapshotDir:  snapshots,
		Platform:     "linux",
		Capabilities: &Capabilities{BindRO: false},
		BindMount: func(string, string) error {
			binds++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if binds != 0 {
		t.Fatalf("a bind was attempted without CAP_SYS_ADMIN (%d calls)", binds)
	}
	if _, serr := os.Stat(snapshots); !os.IsNotExist(serr) {
		t.Fatalf("a snapshot directory was created although no mount could use it: %v", serr)
	}
	modeBinds := modeBitsBind(t)
	if modeBinds {
		if len(result.Warnings) != 0 {
			t.Fatalf("warnings = %v, want none: copy-ro is the correct answer here, not a degradation", result.Warnings)
		}
	} else if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "writable by the account that reads it") {		// Root with CAP_DAC_OVERRIDE is the one host where the probe may only
		// report `copy`; the degradation has to be said, not swallowed.
		t.Fatalf("warnings = %v, want the one honest copy-not-copy-ro line", result.Warnings)
	}

	mounted := result.Mounted[0]
	want := string(MechanismCopyRO)
	if mountedFileIsWritable(mounted.Dir) {
		want = string(MechanismCopy)
	}
	if mounted.Mechanism != want {
		t.Fatalf("mechanism = %q, want %q (writable=%v)", mounted.Mechanism, want, want == string(MechanismCopy))
	}
	// Whatever the probe said, the source bundle is not what the agent sees.
	if filepath.Clean(mounted.Dir) == filepath.Clean(filepath.Join(bundles, "review-1")) {
		t.Fatal("the worktree path is the bundle path")
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
