package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

type env struct {
	t     *testing.T
	svc   *Service
	store *store.Store
	user  *contracts.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	requireGit(t)

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{
		Email: "provision-" + strings.ReplaceAll(t.Name(), "/", "-") + "@example.test",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sb, err := sandbox.NewLocal(sandbox.LocalConfig{Root: filepath.Join(t.TempDir(), "sandboxes")})
	if err != nil {
		t.Fatalf("local sandbox: %v", err)
	}

	svc := New(Config{
		Store:   st,
		Sandbox: sb,
		Git:     gitx.New(gitx.Config{}),
		// A real bundle directory so the mount test exercises copying, not just
		// config writing.
		SkillsDir: filepath.Join(t.TempDir(), "skills"),
	})
	return &env{t: t, svc: svc, store: st, user: user}
}

func (e *env) project() *contracts.Project {
	e.t.Helper()
	p, err := e.store.CreateProject(context.Background(), e.user.ID, store.NewProject{
		Name: "demo", DefaultBranch: "main",
	})
	if err != nil {
		e.t.Fatalf("create project: %v", err)
	}
	return p
}

func (e *env) session(p *contracts.Project, kind contracts.SessionKind, label, parentID string) *contracts.Session {
	e.t.Helper()
	sess, err := e.store.CreateSession(context.Background(), e.user.ID, store.NewSession{
		ProjectID: p.ID, Kind: kind, Label: label, Title: label, ParentID: parentID,
	})
	if err != nil {
		e.t.Fatalf("create session: %v", err)
	}
	return sess
}

func TestEnsureWorktreeMainCreatesSandboxAndCheckout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.project()
	sess := e.session(p, contracts.KindMain, "", "")

	wt, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, sess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if wt.Branch != "main" {
		t.Fatalf("branch = %q, want main", wt.Branch)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, ".git")); err != nil {
		t.Fatalf("worktree has no .git: %v", err)
	}

	// The session must be pointed at it, otherwise the HTTP layer cannot find
	// the root.
	reloaded, err := e.store.Session(ctx, e.user.ID, sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.WorktreeID != wt.ID {
		t.Fatalf("session worktree = %q, want %q", reloaded.WorktreeID, wt.ID)
	}

	// Idempotent: a second call returns the same row and does not add a
	// second checkout.
	again, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, reloaded)
	if err != nil {
		t.Fatalf("second EnsureWorktree: %v", err)
	}
	if again.ID != wt.ID {
		t.Fatalf("idempotency broken: got %q want %q", again.ID, wt.ID)
	}

	list, err := e.store.ListWorktrees(ctx, e.user.ID, wt.SandboxID)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("worktree count = %d, want 1", len(list))
	}
}

func TestEnsureWorktreeBranchGetsItsOwnCheckout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.project()

	main := e.session(p, contracts.KindMain, "", "")
	mainWT, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, main)
	if err != nil {
		t.Fatalf("main: %v", err)
	}

	branch := e.session(p, contracts.KindBranch, "fix-cache", main.ID)
	brWT, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, branch)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}

	if brWT.Branch != "piforge/fix-cache" {
		t.Fatalf("branch = %q, want piforge/fix-cache", brWT.Branch)
	}
	if brWT.Path == mainWT.Path {
		t.Fatalf("branch reused the main checkout at %s", brWT.Path)
	}
	if brWT.SandboxID != mainWT.SandboxID {
		t.Fatalf("branch used a different sandbox (%s vs %s); the whole point is one sandbox, many worktrees",
			brWT.SandboxID, mainWT.SandboxID)
	}
	// Both checkouts must be real worktrees of the same repository.
	if _, err := os.Stat(filepath.Join(brWT.Path, ".git")); err != nil {
		t.Fatalf("branch worktree has no .git: %v", err)
	}
}

func TestEnsureWorktreeSidecarSharesParent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.project()

	main := e.session(p, contracts.KindMain, "", "")
	mainWT, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, main)
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	// Re-read so the parent carries its worktree id, as it will in production.
	main, err = e.store.Session(ctx, e.user.ID, main.ID)
	if err != nil {
		t.Fatalf("reload main: %v", err)
	}

	side := e.session(p, contracts.KindSidecar, "", main.ID)
	sideWT, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, side)
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	if sideWT.ID != mainWT.ID {
		t.Fatalf("sidecar got its own worktree (%s), want the parent's (%s)", sideWT.ID, mainWT.ID)
	}

	reloaded, err := e.store.Session(ctx, e.user.ID, side.ID)
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if reloaded.WorktreeID != mainWT.ID {
		t.Fatalf("sidecar worktree = %q, want %q", reloaded.WorktreeID, mainWT.ID)
	}
}

func TestEnsureWorktreeRejectsForeignUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.project()
	sess := e.session(p, contracts.KindMain, "", "")

	other, err := e.store.CreateUser(ctx, store.NewUser{Email: "intruder@example.test"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if _, err := e.svc.EnsureWorktree(ctx, other.ID, p, sess); err == nil {
		t.Fatal("expected a tenant mismatch error")
	}
}

func TestBranchFor(t *testing.T) {
	project := &contracts.Project{DefaultBranch: "develop"}
	main := &contracts.Session{ID: "ses_01HXYZABCDEF", Kind: contracts.KindMain}
	branchLabeled := &contracts.Session{ID: "ses_01HXYZABCDEF", Kind: contracts.KindBranch, Label: "Fix Cache Race!"}
	branchBare := &contracts.Session{ID: "ses_01HXYZABCDEF", Kind: contracts.KindBranch}

	cases := []struct{ name, got, want string }{
		{"main uses the project branch", BranchFor(project, main), "develop"},
		{"branch is namespaced and slugged", BranchFor(project, branchLabeled), "piforge/fix-cache-race"},
		{"bare branch falls back to the id tail", BranchFor(project, branchBare), "piforge/yzabcdef"},
		{"empty project branch defaults to main", BranchFor(&contracts.Project{}, main), "main"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q want %q", c.name, c.got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix Cache Race":     "fix-cache-race",
		"piforge/foo":        "foo",
		"  --weird__case-- ": "weird-case",
		"":                   "session",
		"!!!":                "session",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 100)
	if got := Slug(long); len(got) > 48 {
		t.Errorf("Slug did not truncate: %d chars", len(got))
	}
}

// TestEnsureWorktreeMountsSkillsAndMCPServers covers the projection step end to
// end: what the user configured must exist as files in the checkout, because
// that is the only thing the agent can see.
func TestEnsureWorktreeMountsSkillsAndMCPServers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A skill bundle on the host.
	bundle := filepath.Join(e.svc.cfg.SkillsDir, "review-1")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte("---\nname: review\n---\nReview diffs.\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := e.store.CreateSkill(ctx, e.user.ID, contracts.Skill{
		Name: "Code Review", BundleKey: "review-1", Version: "1", ContentHash: "h", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if _, err := e.store.CreateMCPServer(ctx, e.user.ID, contracts.MCPServer{
		Name: "playwright", Transport: contracts.MCPStdio,
		Command: "npx", Args: []string{"-y", "@playwright/mcp"}, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	project := e.project()
	sess := e.session(project, contracts.KindMain, "main", "")

	wt, err := e.svc.EnsureWorktree(ctx, e.user.ID, project, sess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}

	skillFile := filepath.Join(wt.Path, ".pi", "skills", "code-review", "SKILL.md")
	if body, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(body), "Review diffs") {
		t.Fatalf("mounted SKILL.md = %q, %v", body, err)
	}

	// The enabled MCP servers are reported, and no MCP file is written into the
	// checkout: `pimodel.WriteMCP` puts the one Pi reads in the session's agent
	// directory, credentials included.
	if _, err := os.Stat(filepath.Join(wt.Path, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("an MCP file appeared in the worktree: %v", err)
	}

	// The manifest must record what was mounted so the API can report it.
	manifest := materialize.Read(wt.Path)
	if len(manifest.Skills) != 1 || manifest.Skills[0] != "code-review" {
		t.Fatalf("manifest skills = %v", manifest.Skills)
	}
	if len(manifest.MCPServers) != 1 || manifest.MCPServers[0] != "playwright" {
		t.Fatalf("manifest mcp servers = %v", manifest.MCPServers)
	}
}
