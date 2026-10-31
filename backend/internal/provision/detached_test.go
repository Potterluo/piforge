package provision

import (
	"context"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/store"
)

// containerFS is the provisioner's view of a workspace that only exists inside a
// sandbox: a store of the paths the projection wrote, and nothing on this disk.
// It fails a test that expects the control plane to be able to open the tree.
type containerFS struct {
	mu      sync.Mutex
	dirs    map[string]bool
	files   map[string][]byte
	lookups []string
}

func newContainerFS() *containerFS {
	return &containerFS{dirs: map[string]bool{"/": true, "/workspace": true}, files: map[string][]byte{}}
}

func (c *containerFS) Join(elems ...string) string { return path.Join(elems...) }
func (c *containerFS) IsAbs(p string) bool         { return strings.HasPrefix(p, "/") }
func (c *containerFS) Abs(p string) (string, error) {
	return p, nil
}
func (c *containerFS) MkdirAll(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs[dir] = true
	return nil
}
func (c *containerFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[p] = append([]byte(nil), data...)
	c.dirs[path.Dir(p)] = true
	return nil
}
func (c *containerFS) ReadFile(p string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}
func (c *containerFS) Lookup(p string) (materialize.Kind, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups = append(c.lookups, p)
	if c.dirs[p] {
		return materialize.KindDir, nil
	}
	if _, ok := c.files[p]; ok {
		return materialize.KindFile, nil
	}
	return materialize.KindMissing, nil
}
func (c *containerFS) List(dir string) ([]materialize.Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	out := []materialize.Entry{}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	for p := range c.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)[0]
		if !seen[name] {
			seen[name] = true
			out = append(out, materialize.Entry{Name: name})
		}
	}
	return out, nil
}
func (c *containerFS) RemoveAll(p string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := strings.TrimSuffix(p, "/") + "/"
	for file := range c.files {
		if file == p || strings.HasPrefix(file, prefix) {
			delete(c.files, file)
		}
	}
	return nil
}
func (c *containerFS) MakeReadOnly(dir string) (bool, error) { return false, nil }
func (c *containerFS) MakeWritable(dir string) error        { return nil }
func (c *containerFS) CanBind() bool                        { return false }

// A detached sandbox (stage 2 of docs/EXECUTION-BOUNDARY.md) has no workspace on
// this filesystem. Provisioning it must therefore be entirely a conversation with
// the sandbox API: the worktree is the provider's business, the skill projection
// writes through the file API, the read-only decision comes from the sandbox, and
// the paths the row keeps are the container's own.
func TestDetachedWorkspaceIsProjectedThroughTheSandboxAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/piforge.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{Email: "detached@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "demo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	branchSess, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Label: "fix-cache", Title: "fix cache",
	})
	if err != nil {
		t.Fatalf("create branch session: %v", err)
	}

	// A skill bundle on the control plane's disk: bundles are uploaded here even
	// when the tree they are projected into is not.
	bundles := t.TempDir()
	if err := os.MkdirAll(bundles+"/pdf-1", 0o755); err != nil {
		t.Fatalf("bundle dir: %v", err)
	}
	if err := os.WriteFile(bundles+"/pdf-1/SKILL.md", []byte("Read PDFs.\n"), 0o644); err != nil {
		t.Fatalf("bundle file: %v", err)
	}
	if _, err := st.CreateSkill(ctx, user.ID, contracts.Skill{
		Name: "PDF Tools", BundleKey: "pdf-1", Version: "1", ContentHash: "hash-pdf", Enabled: true,
	}); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	fake := &fakeWorktreeSandbox{root: "/workspace", workspace: contracts.WorkspaceAPI}
	fs := newContainerFS()
	svc := New(Config{
		Store:   st,
		Sandbox: fake,
		// A git binary that does not exist: a detached workspace must never call
		// the host's git, so taking that path would fail loudly here.
		Git:       gitx.New(gitx.Config{GitBinary: "piforge-test-git-that-does-not-exist"}),
		SkillsDir: bundles,
		WorkspaceFiles: func(ctx context.Context, sb *contracts.Sandbox) (materialize.FileSystem, error) {
			if sb.Workspace != contracts.WorkspaceAPI {
				t.Errorf("the file API was opened for a sandbox that is not detached: %+v", sb)
			}
			return fs, nil
		},
	})

	wt, err := svc.EnsureWorktree(ctx, user.ID, project, branchSess)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if !strings.HasPrefix(wt.Path, "/workspace/worktrees/") {
		t.Fatalf("worktree path = %q, want a container path under /workspace/worktrees", wt.Path)
	}
	if len(fake.calls) != 1 || fake.calls[0].path != wt.Path {
		t.Fatalf("worktree calls = %+v, want one for %q", fake.calls, wt.Path)
	}
	if fake.calls[0].repoDir != "/workspace/repo" {
		t.Fatalf("repo dir = %q, want /workspace/repo", fake.calls[0].repoDir)
	}
	// The projection landed in the sandbox's tree, at the container path.
	mounted := wt.Path + "/.pi/skills/pdf-tools/SKILL.md"
	if body, ok := fs.files[mounted]; !ok || !strings.Contains(string(body), "Read PDFs") {
		t.Fatalf("skill not projected into the sandbox: %v", keysOf(fs.files))
	}
	manifest := materialize.ParseManifest(fs.files[wt.Path+"/.pi/mounted.json"])
	if len(manifest.Skills) != 1 || manifest.Mechanisms["pdf-tools"] != string(materialize.MechanismCopyRO) {
		t.Fatalf("manifest = %+v, want one copy-ro skill", manifest)
	}
	// Nothing was created on this disk for the workspace.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/workspace"); err == nil {
			t.Fatal("the control plane created a /workspace directory")
		}
	}
	// And the session record points the panel at the container tree.
	records, err := st.SessionSkills(ctx, user.ID, branchSess.ID)
	if err != nil {
		t.Fatalf("session skills: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("session skills = %+v", records)
	}
	if records[0].MountedPath != wt.Path+"/.pi/skills/pdf-tools" {
		t.Fatalf("mounted path = %q", records[0].MountedPath)
	}
	if records[0].ContentHash == "" {
		t.Fatal("the mounted skill was not fingerprinted")
	}
}

// A detached sandbox whose path already exists is refused *after asking the
// sandbox*, not after a host stat that can only answer "missing".
func TestDetachedWorkspaceRefusesAnExistingPathThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/piforge.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, err := st.CreateUser(ctx, store.NewUser{Email: "detached-2@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "demo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	sess, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Label: "taken", Title: "taken",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	fake := &fakeWorktreeSandbox{root: "/workspace", workspace: contracts.WorkspaceAPI}
	fs := newContainerFS()
	svc := New(Config{
		Store:   st,
		Sandbox: fake,
		Git:     gitx.New(gitx.Config{}),
		WorkspaceFiles: func(ctx context.Context, sb *contracts.Sandbox) (materialize.FileSystem, error) {
			return fs, nil
		},
	})
	// The path a branch session would use, occupied by something this service did
	// not create.
	occupied := "/workspace/worktrees/" + branchSlug(BranchFor(project, sess))
	fs.dirs[occupied] = true

	_, err = svc.EnsureWorktree(ctx, user.ID, project, sess)
	if err == nil || !strings.Contains(err.Error(), "refusing to reuse") {
		t.Fatalf("EnsureWorktree = %v, want a refusal naming the existing path", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("a worktree was created anyway: %+v", fake.calls)
	}
	for _, looked := range fs.lookups {
		if !strings.HasPrefix(looked, "/workspace/") {
			t.Fatalf("lookup outside the container workspace: %q", looked)
		}
	}
	if len(fs.lookups) == 0 {
		t.Fatal("the existence check never reached the sandbox")
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}



