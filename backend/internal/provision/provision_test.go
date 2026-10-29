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

	// FR-14.5: the session records the version it was actually given. The hash is
	// derived from the mounted files, not copied from the skill row ("h"), which
	// is what makes an undeclared version identifiable.
	records, err := e.store.SessionSkills(ctx, e.user.ID, sess.ID)
	if err != nil {
		t.Fatalf("SessionSkills: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("session skill records = %d, want 1", len(records))
	}
	got := records[0]
	if got.SkillID == "" || got.Name != "Code Review" || got.Version != "1" {
		t.Fatalf("record identity = %+v", got)
	}
	wantHash, err := materialize.Digest(filepath.Join(wt.Path, ".pi", "skills", "code-review"))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got.ContentHash != wantHash || got.ContentHash == "h" {
		t.Fatalf("record hash = %q, want the mounted bundle's digest %q", got.ContentHash, wantHash)
	}
	if want := filepath.ToSlash(filepath.Join(wt.Path, ".pi", "skills", "code-review")); got.MountedPath != want {
		t.Fatalf("mounted path = %q, want %q", got.MountedPath, want)
	}
	if got.RecordedAt.IsZero() {
		t.Fatal("recorded_at is zero")
	}
}

// TestMountRecordsTheVersionOncePerSession covers the FR-14.5 invariant across a
// skill edit: the session created before the edit keeps reporting the old
// version, while a session created after it records the new one.
func TestMountRecordsTheVersionOncePerSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	bundle := filepath.Join(e.svc.cfg.SkillsDir, "review-1")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte("# Review\nRead the diff.\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	skill, err := e.store.CreateSkill(ctx, e.user.ID, contracts.Skill{
		Name: "Code Review", BundleKey: "review-1", Version: "1", ContentHash: "hash-v1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	projectA := e.project()
	sessA := e.session(projectA, contracts.KindMain, "main", "")
	wtA, err := e.svc.EnsureWorktree(ctx, e.user.ID, projectA, sessA)
	if err != nil {
		t.Fatalf("EnsureWorktree A: %v", err)
	}
	before, err := e.store.SessionSkills(ctx, e.user.ID, sessA.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("session A records = %v, %v", before, err)
	}

	// Edit the skill: new bundle content and a new stored hash.
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte("# Review\nRead every hunk twice.\n"), 0o644); err != nil {
		t.Fatalf("rewrite SKILL.md: %v", err)
	}
	if _, err := e.store.UpdateSkillBundle(ctx, e.user.ID, skill.ID, "review-1", "hash-v2"); err != nil {
		t.Fatalf("UpdateSkillBundle: %v", err)
	}

	projectB, err := e.store.CreateProject(ctx, e.user.ID, store.NewProject{Name: "demo-2", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project B: %v", err)
	}
	sessB := e.session(projectB, contracts.KindMain, "main", "")
	wtB, err := e.svc.EnsureWorktree(ctx, e.user.ID, projectB, sessB)
	if err != nil {
		t.Fatalf("EnsureWorktree B: %v", err)
	}
	after, err := e.store.SessionSkills(ctx, e.user.ID, sessB.ID)
	if err != nil || len(after) != 1 {
		t.Fatalf("session B records = %v, %v", after, err)
	}

	newHash, err := materialize.Digest(filepath.Join(wtB.Path, ".pi", "skills", "code-review"))
	if err != nil {
		t.Fatalf("Digest B: %v", err)
	}
	if after[0].ContentHash != newHash {
		t.Fatalf("session B hash = %q, want %q", after[0].ContentHash, newHash)
	}
	if before[0].ContentHash == newHash {
		t.Fatal("the two sessions recorded the same version despite different content")
	}

	// Session A's record is a fact and does not drift, even though the code path
	// that wrote it is gone.
	rereadA, err := e.store.SessionSkills(ctx, e.user.ID, sessA.ID)
	if err != nil {
		t.Fatalf("SessionSkills A again: %v", err)
	}
	if rereadA[0].ContentHash != before[0].ContentHash || rereadA[0].MountedPath != before[0].MountedPath {
		t.Fatalf("session A's record changed: %+v -> %+v", before[0], rereadA[0])
	}
	if wtA.Path == wtB.Path {
		t.Fatal("the two projects shared one checkout, so this test mounted the same directory twice")
	}
}

// fakeWorktreeSandbox is a SandboxProvider that can also run its own worktree
// creation — the shape -sandbox docker has (contracts.WorktreeSandbox).
type fakeWorktreeSandbox struct {
	root  string
	calls []worktreeCall
}

type worktreeCall struct {
	sandboxID string
	repoDir   string
	path      string
	branch    string
}

var _ contracts.SandboxProvider = (*fakeWorktreeSandbox)(nil)
var _ contracts.WorktreeSandbox = (*fakeWorktreeSandbox)(nil)

func (f *fakeWorktreeSandbox) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	return &contracts.Sandbox{
		ID:        "sbx_fake",
		UserID:    userID,
		ProjectID: projectID,
		State:     contracts.SandboxReady,
		Provider:  "fake",
		RootDir:   f.root,
	}, nil
}

func (f *fakeWorktreeSandbox) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	return f.Ensure(ctx, "", "", "", "")
}

func (f *fakeWorktreeSandbox) Pause(ctx context.Context, sandboxID string) error { return nil }

func (f *fakeWorktreeSandbox) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	return f.Get(ctx, sandboxID)
}

func (f *fakeWorktreeSandbox) Destroy(ctx context.Context, sandboxID string) error { return nil }

func (f *fakeWorktreeSandbox) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	return "", nil
}

func (f *fakeWorktreeSandbox) AddWorktree(ctx context.Context, sandboxID, repoDir, path, branch string) error {
	f.calls = append(f.calls, worktreeCall{sandboxID: sandboxID, repoDir: repoDir, path: path, branch: branch})
	return nil
}

// The choice between the provider's own worktree creation and the control
// plane's git follows the provider, not a flag: a provider that implements
// contracts.WorktreeSandbox creates the worktree itself. The host runner here
// points at a git binary that does not exist, so taking the host path would fail
// loudly instead of silently producing the root-owned worktree that made stage
// die under -sandbox docker.
func TestBranchWorktreeUsesTheProvider(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{Email: "provider-choice@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "demo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	branch, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Label: "fix-cache", Title: "fix cache",
	})
	if err != nil {
		t.Fatalf("create branch session: %v", err)
	}

	root := t.TempDir()
	fake := &fakeWorktreeSandbox{root: root}
	svc := New(Config{
		Store:   st,
		Sandbox: fake,
		Git:     gitx.New(gitx.Config{GitBinary: "piforge-test-git-that-does-not-exist"}),
	})

	wt, err := svc.EnsureWorktree(ctx, user.ID, project, branch)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("the provider was asked %d times, want exactly one worktree: %+v", len(fake.calls), fake.calls)
	}
	call := fake.calls[0]
	if call.sandboxID != "sbx_fake" {
		t.Fatalf("sandbox id = %q", call.sandboxID)
	}
	if filepath.Clean(call.repoDir) != filepath.Join(root, "repo") {
		t.Fatalf("repo dir = %q, want the main checkout inside the sandbox", call.repoDir)
	}
	wantPath := filepath.Join(root, "worktrees", "fix-cache")
	if filepath.Clean(call.path) != wantPath {
		t.Fatalf("worktree path = %q, want %q", call.path, wantPath)
	}
	if call.branch != "piforge/fix-cache" {
		t.Fatalf("branch = %q, want the namespaced branch", call.branch)
	}
	if filepath.Clean(wt.Path) != wantPath || wt.Branch != "piforge/fix-cache" {
		t.Fatalf("recorded worktree = %+v, want path %q on piforge/fix-cache", wt, wantPath)
	}
	// The host git never touched the workspace: no host-side checkout appeared.
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("the host path was created outside the provider: %v", err)
	}
}

// The product defect, end to end: under -sandbox docker a branch session's
// worktree used to be created by the host's git through the bind mount, so
// `<main>/.git/worktrees/<slug>` belonged to the host user while the container's
// git runs as `gem`, and `git stage` died with
//
//	fatal: Unable to create '.../.git/worktrees/<slug>/index.lock': Permission denied
//
// This test provisions a real branch session through the provisioner, then does
// exactly what the panel's stage and commit endpoints do — as `gem`, inside the
// container — and checks the ownership that made it possible.
func TestBranchWorktreeInDockerStagesAndCommitsAsGem(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("PIFORGE_TEST_SANDBOX_IMAGE"))
	if image == "" {
		t.Skip("PIFORGE_TEST_SANDBOX_IMAGE is not set: this test needs a sandbox image with git and the gem user (all-in-one-sandbox 1.11.0)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("the docker daemon is not reachable: %v", err)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "piforge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{Email: "docker-branch@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "docker-branch", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	main, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindMain, Title: "main",
	})
	if err != nil {
		t.Fatalf("create main session: %v", err)
	}
	branch, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Label: "docker-branch", Title: "docker branch",
	})
	if err != nil {
		t.Fatalf("create branch session: %v", err)
	}

	provider, err := sandbox.NewDocker(sandbox.DockerConfig{
		Image:    image,
		DataRoot: filepath.Join(t.TempDir(), "sandboxes"),
		CPUs:     "1",
		Memory:   "1g",
		APIPort:  8080,
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	svc := New(Config{
		Store:     st,
		Sandbox:   provider,
		Git:       gitx.New(gitx.Config{}),
		SkillsDir: filepath.Join(t.TempDir(), "skills"),
	})

	mainWT, err := svc.EnsureWorktree(ctx, user.ID, project, main)
	if err != nil {
		t.Fatalf("main worktree: %v", err)
	}
	t.Cleanup(func() { _ = provider.Destroy(context.Background(), mainWT.SandboxID) })

	brWT, err := svc.EnsureWorktree(ctx, user.ID, project, branch)
	if err != nil {
		t.Fatalf("branch worktree: %v", err)
	}

	const (
		containerRepo = "/workspace/repo"
		containerWT   = "/workspace/worktrees/docker-branch"
	)
	wantPath := filepath.Join(filepath.Dir(mainWT.Path), "worktrees", "docker-branch")
	if filepath.Clean(brWT.Path) != filepath.Clean(wantPath) {
		t.Fatalf("branch worktree path = %q, want %q", brWT.Path, wantPath)
	}
	if brWT.Branch != "piforge/docker-branch" {
		t.Fatalf("branch = %q, want piforge/docker-branch", brWT.Branch)
	}
	if brWT.SandboxID != mainWT.SandboxID {
		t.Fatalf("branch used sandbox %q, want the project's %q", brWT.SandboxID, mainWT.SandboxID)
	}
	if got, err := provider.ContainerPath(brWT.Path); err != nil || got != containerWT {
		t.Fatalf("ContainerPath(%q) = %q, %v; want %q", brWT.Path, got, err, containerWT)
	}

	// A real linked worktree of the main checkout, not a copy: git's own
	// bookkeeping lists it, and the .git file points at the container's path (the
	// host path the old implementation wrote is what the container could not
	// resolve).
	list, err := dockerAsGem(t, brWT.SandboxID, "git", "-C", containerRepo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list as gem: %v: %s", err, list)
	}
	if !strings.Contains(list, "worktree "+containerWT) {
		t.Fatalf("the branch checkout is not a worktree of the main checkout:\n%s", list)
	}
	gitFile, err := os.ReadFile(filepath.Join(brWT.Path, ".git"))
	if err != nil {
		t.Fatalf("read the worktree's .git file: %v", err)
	}
	if !strings.Contains(string(gitFile), containerRepo+"/.git/worktrees/docker-branch") {
		t.Fatalf(".git = %q, want the container's gitdir", strings.TrimSpace(string(gitFile)))
	}

	// Ownership is the defect: these two directories are written by the panel's
	// git, which runs as gem.
	for _, dir := range []string{containerRepo + "/.git/worktrees/docker-branch", containerWT} {
		owner, err := dockerAsGem(t, brWT.SandboxID, "stat", "-c", "%U", dir)
		if err != nil {
			t.Fatalf("stat %s: %v: %s", dir, err, owner)
		}
		if owner != "gem" {
			t.Fatalf("%s is owned by %q, want gem: stage would fail on index.lock", dir, owner)
		}
	}

	// What GET /git/status, POST /git/stage and POST /git/commit do, as the same
	// account: this is the failure that was reported.
	status, err := dockerAsGem(t, brWT.SandboxID, "git", "-C", containerWT, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status as gem: %v: %s", err, status)
	}
	out, err := dockerAsGem(t, brWT.SandboxID, "sh", "-c",
		"cd "+containerWT+" && printf 'branch work from the panel\\n' > branch.txt && "+
			"git add -A && git commit -q -m 'feat: branch commit as gem' && git log --oneline -1")
	if err != nil {
		t.Fatalf("stage and commit as gem: %v: %s", err, out)
	}
	if !strings.Contains(out, "feat: branch commit as gem") {
		t.Fatalf("log after commit = %q", out)
	}
	if status, err := dockerAsGem(t, brWT.SandboxID, "git", "-C", containerWT, "status", "--porcelain"); err != nil || status != "" {
		t.Fatalf("status after commit = %q, %v; want a clean worktree", status, err)
	}
	if name, err := dockerAsGem(t, brWT.SandboxID, "git", "-C", containerWT, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || name != "piforge/docker-branch" {
		t.Fatalf("HEAD branch = %q, %v; want piforge/docker-branch", name, err)
	}

	// Existing sessions are not silently rewritten: asking again returns the same
	// row, and the worktree keeps its commit.
	reloaded, err := st.Session(ctx, user.ID, branch.ID)
	if err != nil {
		t.Fatalf("reload branch session: %v", err)
	}
	again, err := svc.EnsureWorktree(ctx, user.ID, project, reloaded)
	if err != nil {
		t.Fatalf("second EnsureWorktree: %v", err)
	}
	if again.ID != brWT.ID || filepath.Clean(again.Path) != filepath.Clean(brWT.Path) {
		t.Fatalf("second EnsureWorktree changed the worktree: %+v vs %+v", again, brWT)
	}
	head, err := dockerAsGem(t, brWT.SandboxID, "git", "-C", containerWT, "log", "--oneline", "-1")
	if err != nil || !strings.Contains(head, "feat: branch commit as gem") {
		t.Fatalf("worktree HEAD after a repeated provision = %q, %v", head, err)
	}
}

// dockerAsGem runs argv in the container as the sandbox user. Docker is only an
// inspection tool in these tests; provisioning itself goes through the sandbox
// API.
func dockerAsGem(t *testing.T, sandboxID string, argv ...string) (string, error) {
	t.Helper()
	args := append([]string{"exec", "-u", "gem", sandboxID}, argv...)
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
