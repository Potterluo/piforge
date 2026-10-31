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
	if mechanism != string(materialize.MechanismCopyRO) {
		// The only other honest answer is a bind mount on Linux; a test host
		// without privileges must degrade, never claim the mount.
		if mechanism != string(materialize.MechanismBindRO) || runtime.GOOS != "linux" {
			t.Fatalf("mechanism = %q on %s", mechanism, runtime.GOOS)
		}
	}

	// A write to the mounted file is refused, and the mounted copy is unchanged.
	skillFile := filepath.Join(mounted, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("agent was here\n"), 0o644); err == nil {
		t.Fatal("the mounted skill was writable from the control plane's filesystem")
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
	out, execErr := e.svc.cfg.Sandbox.Exec(ctx, wt.SandboxID, argv, wt.Path)
	if execErr == nil {
		t.Fatalf("the sandbox wrote the mounted skill (output %q)", out)
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
