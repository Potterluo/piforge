package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/store"
)

// seedSkillRow puts one bundle on disk and registers the row.
func seedSkillRow(t *testing.T, e *env, bundleKey, body string) *contracts.Skill {
	t.Helper()
	dir := filepath.Join(e.svc.cfg.SkillsDir, bundleKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	skill, err := e.store.CreateSkill(context.Background(), e.user.ID, contracts.Skill{
		Name: "Code Review", BundleKey: bundleKey, Version: "1", ContentHash: "hash-" + bundleKey, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	return skill
}

// modeBitsBind asks this host the only question that decides whether `copy-ro`
// is an honest label: does a 0444 file really refuse a write to *this* process?
//
// The answer is platform- and uid-dependent. Windows maps the bit to
// FILE_ATTRIBUTE_READONLY, which binds everyone; Linux binds the owner, but root
// carrying CAP_DAC_OVERRIDE ignores it — which is `go test ./...` on a stock
// Linux root host, the host this suite was first run on in the Linux pass
// (docs/LINUX-VERIFY.md, row L9). Hard-coding copy-ro made the suite red there
// for a reason that was true and not a bug.
func modeBitsBind(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte("probe\n"), 0o644); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatalf("probe chmod: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("probe chmod dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(file, 0o644)
	})
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		return true
	}
	_ = f.Close()
	return false
}

// FR-14.2: what the worktree gets is not a pointer at the uploaded bundle. The
// mount decision is recorded (`copy-ro` on Windows, `bind-ro` where a bind is
// possible and permitted), and a write through the mounted path is refused
// without touching the source.
func TestMountedSkillIsNotTheBundleAndIsReadOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seedSkillRow(t, e, "review-1", "# Review\nRead the diff.\n")
	sourceDir := filepath.Join(e.svc.cfg.SkillsDir, "review-1")

	project := e.project()
	sess := e.session(project, contracts.KindMain, "main", "")
	wt, err := e.svc.EnsureWorktree(ctx, e.user.ID, project, sess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}

	mounted := filepath.Join(wt.Path, ".pi", "skills", "code-review")
	if filepath.Clean(mounted) == filepath.Clean(sourceDir) {
		t.Fatal("the worktree path is the bundle path")
	}
	manifest := materialize.Read(wt.Path)
	mechanism := manifest.Mechanisms["code-review"]
	binds := modeBitsBind(t)
	switch {
	case mechanism == string(materialize.MechanismBindRO):
		// The kernel enforces the mount; nothing can write through it.
	case mechanism == string(materialize.MechanismCopyRO) && binds:
		// The default deployment: a copy whose 0444/0555 bits bind this process.
	case mechanism == string(materialize.MechanismCopy) && !binds:
		// A host where the bits do not bind (root with CAP_DAC_OVERRIDE). The
		// mechanism says `copy` instead of claiming `copy-ro`, which is the
		// honest answer the product is supposed to give here.
	default:
		t.Fatalf("mechanism = %q on %s (the read-only bits bind this process: %v)", mechanism, runtime.GOOS, binds)
	}

	// A write to the mounted file is refused where the platform enforces it, and
	// the mounted copy plus the source stay byte-for-byte what they were.
	skillFile := filepath.Join(mounted, "SKILL.md")
	if mechanism != string(materialize.MechanismCopy) {
		if err := os.WriteFile(skillFile, []byte("agent was here\n"), 0o644); err == nil {
			t.Fatal("the mounted skill was writable from the control plane's filesystem")
		}
	}
	if body, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("mounted SKILL.md = %q, %v", body, err)
	}
	// The source bundle was not the thing that was written to.
	if body, err := os.ReadFile(filepath.Join(e.svc.cfg.SkillsDir, "review-1", "SKILL.md")); err != nil ||
		!strings.Contains(string(body), "Read the diff") {
		t.Fatalf("source bundle = %q, %v", body, err)
	}
}

// FR-14.2: an edit reaches only sessions provisioned after it. Re-provisioning a
// running session must not rewrite its mount, while a new session sees the new
// content — the two halves of the requirement that pull in opposite directions.
func TestEditingASkillDoesNotReachARunningSessionButANewOneGetsIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	skill := seedSkillRow(t, e, "review-1", "# Review\nVersion one.\n")

	projectA := e.project()
	sessA := e.session(projectA, contracts.KindMain, "main", "")
	wtA, err := e.svc.EnsureWorktree(ctx, e.user.ID, projectA, sessA)
	if err != nil {
		t.Fatalf("EnsureWorktree A: %v", err)
	}
	mountedA := filepath.Join(wtA.Path, ".pi", "skills", "code-review", "SKILL.md")
	if body, err := os.ReadFile(mountedA); err != nil || !strings.Contains(string(body), "Version one") {
		t.Fatalf("session A mount = %q, %v", body, err)
	}

	// Edit the bundle in place, as the skill centre's upload path does.
	if err := os.WriteFile(filepath.Join(e.svc.cfg.SkillsDir, "review-1", "SKILL.md"),
		[]byte("# Review\nVersion two.\n"), 0o644); err != nil {
		t.Fatalf("edit bundle: %v", err)
	}
	if _, err := e.store.UpdateSkillBundle(ctx, e.user.ID, skill.ID, "review-1", "hash-v2"); err != nil {
		t.Fatalf("UpdateSkillBundle: %v", err)
	}

	// Re-provisioning session A (the same session, same worktree) must not
	// refresh what it is running with.
	if _, err := e.svc.EnsureWorktree(ctx, e.user.ID, projectA, sessA); err != nil {
		t.Fatalf("re-provision A: %v", err)
	}
	if body, err := os.ReadFile(mountedA); err != nil || !strings.Contains(string(body), "Version one") {
		t.Fatalf("an edit reached a running session: %q, %v", body, err)
	}

	projectB := e.project2()
	sessB := e.session(projectB, contracts.KindMain, "main", "")
	wtB, err := e.svc.EnsureWorktree(ctx, e.user.ID, projectB, sessB)
	if err != nil {
		t.Fatalf("EnsureWorktree B: %v", err)
	}
	mountedB := filepath.Join(wtB.Path, ".pi", "skills", "code-review", "SKILL.md")
	if body, err := os.ReadFile(mountedB); err != nil || !strings.Contains(string(body), "Version two") {
		t.Fatalf("a new session did not get the edit: %q, %v", body, err)
	}
}

// FR-14.2, end to end: the check runs through the sandbox's own Exec path — the
// same one the agent's shell uses — instead of writing from the control plane.
// A write from inside the sandbox is refused and the uploaded bundle is
// byte-for-byte unchanged.
func TestSandboxExecCannotWriteTheMountedSkill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seedSkillRow(t, e, "review-1", "# Review\nRead the diff.\n")

	project := e.project()
	sess := e.session(project, contracts.KindMain, "main", "")
	wt, err := e.svc.EnsureWorktree(ctx, e.user.ID, project, sess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}

	var argv []string
	rel := filepath.Join(".pi", "skills", "code-review", "SKILL.md")
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "echo hacked > " + rel}
	} else {
		argv = []string{"sh", "-c", "echo hacked > " + rel}
	}
	// A sandbox exec runs as the same account as this test on the local
	// placement, so the refusal is real exactly where the mode bits bind this
	// process (or where the kernel enforces a bind mount). Where they do not —
	// root with CAP_DAC_OVERRIDE — the mechanism is recorded as `copy` and the
	// write below is expected to succeed; asserting otherwise would only be
	// testing the uid the test happens to run as.
	enforced := modeBitsBind(t) ||
		materialize.Read(wt.Path).Mechanisms["code-review"] == string(materialize.MechanismBindRO)
	out, execErr := e.svc.cfg.Sandbox.Exec(ctx, wt.SandboxID, argv, wt.Path)
	if enforced && execErr == nil {
		t.Fatalf("the sandbox wrote the mounted skill (output %q)", out)
	}
	if !enforced {
		// Put the file back so the assertions below still describe a copy that
		// was handed to the agent, not one this test clobbered.
		_, _ = e.svc.cfg.Sandbox.Exec(ctx, wt.SandboxID,
			[]string{"sh", "-c", "chmod u+w " + rel + " && printf '# Review\\nRead the diff.\\n' > " + rel}, wt.Path)
	}

	// The mounted copy still has the original instructions, and so does the
	// source the next session will be provisioned from.
	mounted, err := os.ReadFile(filepath.Join(wt.Path, ".pi", "skills", "code-review", "SKILL.md"))
	if err != nil || !strings.Contains(string(mounted), "Read the diff") {
		t.Fatalf("mounted SKILL.md after the sandbox write = %q, %v", mounted, err)
	}
	if body, err := os.ReadFile(filepath.Join(e.svc.cfg.SkillsDir, "review-1", "SKILL.md")); err != nil ||
		!strings.Contains(string(body), "Read the diff") {
		t.Fatalf("source bundle after the sandbox write = %q, %v", body, err)
	}

	// The refusal is about writing, not about access: the agent can still read
	// the skill it was given.
	readArgv := []string{"cat", filepath.Join(".pi", "skills", "code-review", "SKILL.md")}
	if runtime.GOOS == "windows" {
		readArgv = []string{"cmd", "/c", "type " + rel}
	}
	readOut, readErr := e.svc.cfg.Sandbox.Exec(ctx, wt.SandboxID, readArgv, wt.Path)
	if readErr != nil || !strings.Contains(readOut, "Read the diff") {
		if _, lookErr := exec.LookPath(readArgv[0]); lookErr != nil {
			t.Skipf("%s is not available: %v", readArgv[0], lookErr)
		}
		t.Fatalf("the agent could not read its mounted skill: %q, %v", readOut, readErr)
	}
}

// project2 is a second project in the same account: sessions in different
// projects get different worktrees, which is what makes "new session, new copy"
// observable.
func (e *env) project2() *contracts.Project {
	e.t.Helper()
	p, err := e.store.CreateProject(context.Background(), e.user.ID, store.NewProject{
		Name: "demo-2", DefaultBranch: "main",
	})
	if err != nil {
		e.t.Fatalf("create project 2: %v", err)
	}
	return p
}

// A checkout can lose its projection without losing its row: a container
// restart unmounts the bind mount and leaves an empty `.pi/skills`, and a
// cleanup can delete the directory. The session is still usable, so the next
// provisioning pass has to put the skills back — the early "already attached"
// return used to skip the mount entirely, and such a session kept no skills
// forever.
func TestExistingWorktreeGetsItsSkillsBackWhenTheProjectionIsMissing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seedSkillRow(t, e, "review-1", "# Review\nRead the diff.\n")

	project := e.project()
	sess := e.session(project, contracts.KindMain, "main", "")
	wt, err := e.svc.EnsureWorktree(ctx, e.user.ID, project, sess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	mounted := filepath.Join(wt.Path, ".pi", "skills", "code-review", "SKILL.md")
	if _, err := os.Stat(mounted); err != nil {
		t.Fatalf("the first pass did not mount the skill: %v", err)
	}

	// Simulate the lost mount: the file is gone, the session row and worktree
	// are not.
	if err := os.Chmod(mounted, 0o644); err != nil {
		t.Fatalf("chmod mounted skill: %v", err)
	}
	if err := os.Remove(mounted); err != nil {
		t.Fatalf("remove mounted skill: %v", err)
	}

	if _, err := e.svc.EnsureWorktree(ctx, e.user.ID, project, sess); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	body, err := os.ReadFile(mounted)
	if err != nil || !strings.Contains(string(body), "Read the diff") {
		t.Fatalf("the missing projection was not repaired: %q, %v", body, err)
	}
}
