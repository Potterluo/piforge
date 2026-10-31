package materialize

import (
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/mcpstage"
)

// memFS is a FileSystem with no disk under it: everything MountFS writes has to
// arrive through this interface, so a test that inspects it is a test that the
// projection really is placement-agnostic (stage 2 of
// docs/EXECUTION-BOUNDARY.md). It models the parts that matter — a tree, mode
// bits, and the read-only probe's answer — and nothing else.
type memFS struct {
	mu            sync.Mutex
	dirs          map[string]bool
	files         map[string][]byte
	readOnly      map[string]bool
	writableProbe bool
	calls         []string
}

func newMemFS() *memFS {
	return &memFS{dirs: map[string]bool{"/": true}, files: map[string][]byte{}, readOnly: map[string]bool{}}
}

func (m *memFS) note(call string) {
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
}

// Calls returns the operations this fake saw, in order.
func (m *memFS) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *memFS) Join(elems ...string) string {
	// path.Join, because that is what the sandbox filesystem does: a container
	// path is POSIX, and safeJoin hands over an element that starts with "/".
	return path.Join(elems...)
}

func (m *memFS) IsAbs(p string) bool { return strings.HasPrefix(p, "/") }

func (m *memFS) Abs(p string) (string, error) { return p, nil }

func (m *memFS) MkdirAll(dir string) error {
	m.note("mkdir " + dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[dir] = true
	return nil
}

func (m *memFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	m.note("write " + p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly[p] {
		return os.ErrPermission
	}
	m.files[p] = append([]byte(nil), data...)
	// The image creates missing parents, which is what makes a per-file
	// projection possible.
	dir := p
	for {
		idx := strings.LastIndex(dir, "/")
		if idx <= 0 {
			break
		}
		dir = dir[:idx]
		m.dirs[dir] = true
	}
	return nil
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	m.note("read " + p)
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *memFS) Lookup(p string) (Kind, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs[p] {
		return KindDir, nil
	}
	if _, ok := m.files[p]; ok {
		return KindFile, nil
	}
	return KindMissing, nil
}

func (m *memFS) List(dir string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	seen := map[string]bool{}
	out := []Entry{}
	for p := range m.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Entry{Name: name, IsDir: m.dirs[prefix+name]})
	}
	for p := range m.dirs {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Entry{Name: name, IsDir: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memFS) RemoveAll(p string) error {
	m.note("remove " + p)
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(p, "/") + "/"
	for file := range m.files {
		if file == p || strings.HasPrefix(file, prefix) {
			delete(m.files, file)
			delete(m.readOnly, file)
		}
	}
	for dir := range m.dirs {
		if dir == p || strings.HasPrefix(dir, prefix) {
			delete(m.dirs, dir)
		}
	}
	return nil
}

func (m *memFS) MakeReadOnly(dir string) (bool, error) {
	m.note("chmod-ro " + dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	for file := range m.files {
		if file == dir || strings.HasPrefix(file, prefix) {
			m.readOnly[file] = true
		}
	}
	return m.writableProbe, nil
}

func (m *memFS) MakeWritable(dir string) error {
	m.note("chmod-rw " + dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	for file := range m.readOnly {
		if file == dir || strings.HasPrefix(file, prefix) {
			delete(m.readOnly, file)
		}
	}
	return nil
}

func (m *memFS) CanBind() bool { return false }

// The projection has to work with nothing but the FileSystem: this is the stage-2
// shape (the tree lives inside the sandbox), and the fake is the proof that no
// step of it reaches for the host.
func TestMountFSProjectsEntirelyThroughTheFilesystem(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "pdf-1", "---\nname: pdf\n---\nRead PDFs.\n")

	fs := newMemFS()
	root := "/workspace/repo"
	result, err := MountFS(fs, root, []*contracts.Skill{
		{ID: "skl_1", Name: "PDF Tools", BundleKey: "pdf-1", Enabled: true},
	}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("MountFS: %v", err)
	}
	if len(result.Mounted) != 1 {
		t.Fatalf("mounted = %+v, want one skill", result.Mounted)
	}
	mounted := result.Mounted[0]
	if mounted.Dir != "/workspace/repo/.pi/skills/pdf-tools" {
		t.Fatalf("mounted dir = %q", mounted.Dir)
	}
	// A file API has no mount namespace, so the honest mechanism is a read-only
	// copy — and the record says so.
	if mounted.Mechanism != string(MechanismCopyRO) {
		t.Fatalf("mechanism = %q, want %q", mounted.Mechanism, MechanismCopyRO)
	}
	if body := fs.files["/workspace/repo/.pi/skills/pdf-tools/SKILL.md"]; !strings.Contains(string(body), "Read PDFs") {
		t.Fatalf("SKILL.md = %q", body)
	}
	manifest := ReadFS(fs, root)
	if len(manifest.Skills) != 1 || manifest.Skills[0] != "pdf-tools" {
		t.Fatalf("manifest skills = %v", manifest.Skills)
	}
	if manifest.Mechanisms["pdf-tools"] != string(MechanismCopyRO) {
		t.Fatalf("manifest mechanisms = %v", manifest.Mechanisms)
	}
	if manifest.SkillIDs["pdf-tools"] != "skl_1" {
		t.Fatalf("manifest skill ids = %v", manifest.SkillIDs)
	}

	// The frozen copy is read-only: writing into it is refused, which is what the
	// agent would experience.
	if err := fs.WriteFile("/workspace/repo/.pi/skills/pdf-tools/SKILL.md", []byte("changed"), 0o644); err == nil {
		t.Fatal("the mounted copy accepted a write")
	}

	// A second pass keeps the frozen copy (same skill row) instead of replacing it.
	fs.calls = nil
	if _, err := MountFS(fs, root, []*contracts.Skill{
		{ID: "skl_1", Name: "PDF Tools", BundleKey: "pdf-1", Enabled: true},
	}, nil, Options{SkillsDir: bundles}); err != nil {
		t.Fatalf("second MountFS: %v", err)
	}
	for _, call := range fs.Calls() {
		if strings.HasPrefix(call, "write /workspace/repo/.pi/skills/pdf-tools") {
			t.Fatalf("a second pass rewrote the frozen copy: %q", call)
		}
	}

	// Dropping the skill removes its directory through the filesystem.
	if _, err := MountFS(fs, root, nil, nil, Options{SkillsDir: bundles}); err != nil {
		t.Fatalf("empty MountFS: %v", err)
	}
	if _, ok := fs.files["/workspace/repo/.pi/skills/pdf-tools/SKILL.md"]; ok {
		t.Fatal("the stale skill is still there")
	}
}

// Stage 4's stdio decision, recorded where a user can find it: a server the
// placement cannot stage is in the manifest's mcp_unavailable map with its code
// and reason, and deliberately *not* in mcp_servers (that list is what the agent
// was given). An HTTP server in the same configuration is unaffected.
func TestMountFSRecordsAnUnstagedServerInsteadOfClaimingIt(t *testing.T) {
	fs := newMemFS()
	root := "/workspace/repo"
	servers := []*contracts.MCPServer{
		{Name: "fs", Enabled: true, Transport: contracts.MCPStdio, Command: "npx"},
		{Name: "docs", Enabled: true, Transport: contracts.MCPHTTP, URL: "https://example.test/mcp"},
	}
	omission := mcpstage.Omission{Code: mcpstage.CodeStdioNeedsCredentials, Reason: mcpstage.ReasonStdio}
	if _, err := MountFS(fs, root, nil, servers, Options{MCPUnavailable: map[string]mcpstage.Omission{"fs": omission}}); err != nil {
		t.Fatalf("MountFS: %v", err)
	}
	manifest := ReadFS(fs, root)
	if len(manifest.MCPServers) != 1 || manifest.MCPServers[0] != "docs" {
		t.Fatalf("mcp_servers = %v, want only the HTTP server", manifest.MCPServers)
	}
	if !manifest.MCPWritten {
		t.Fatal("mcp_written is false although one server was staged")
	}
	recorded, ok := manifest.MCPUnavailable["fs"]
	if !ok || recorded.Code != omission.Code || recorded.Reason != omission.Reason {
		t.Fatalf("mcp_unavailable = %v, want the stdio omission recorded", manifest.MCPUnavailable)
	}
}

// A privileged account defeats mode bits, and the mechanism has to say "copy"
// rather than claim a read-only guarantee it does not have. The same probe runs in
// the sandbox for a container placement; this pins the decision, not the transport.
func TestMountFSReportsAWritableCopy(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "a", "alpha\n")

	fs := newMemFS()
	fs.writableProbe = true
	result, err := MountFS(fs, "/workspace/repo", []*contracts.Skill{
		{ID: "skl_1", Name: "alpha", BundleKey: "a", Enabled: true},
	}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("MountFS: %v", err)
	}
	if got := result.Mounted[0].Mechanism; got != string(MechanismCopy) {
		t.Fatalf("mechanism = %q, want %q", got, MechanismCopy)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "not read-only") {
		t.Fatalf("warnings = %v, want one naming the writable copy", result.Warnings)
	}
}

// DigestFS is the fingerprint of what the sandbox stores, read back through the
// API: the same bytes as the bundle produce the same digest as the host walk.
func TestDigestFSMatchesTheHostDigest(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, bundles, "a", "alpha\n")
	writeBundle(t, bundles, "b", "beta\n")

	fs := newMemFS()
	root := "/workspace/repo"
	if _, err := MountFS(fs, root, []*contracts.Skill{
		{ID: "skl_a", Name: "alpha", BundleKey: "a", Enabled: true},
		{ID: "skl_b", Name: "beta", BundleKey: "b", Enabled: true},
	}, nil, Options{SkillsDir: bundles}); err != nil {
		t.Fatalf("MountFS: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		remote, err := DigestFS(fs, fs.Join(root, SkillsRoot, name))
		if err != nil {
			t.Fatalf("DigestFS(%s): %v", name, err)
		}
		host, err := Digest(bundles + "/" + map[string]string{"alpha": "a", "beta": "b"}[name])
		if err != nil {
			t.Fatalf("Digest(%s): %v", name, err)
		}
		if remote != host {
			t.Fatalf("%s: digest through the API = %s, on disk = %s", name, remote, host)
		}
	}
}
