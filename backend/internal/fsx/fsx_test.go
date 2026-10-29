package fsx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func mustWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func findNode(nodes []*contracts.FileNode, name string) *contracts.FileNode {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func TestTreeBasics(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	mustWrite(t, root, "main.go", "package main\n")
	mustWrite(t, root, "sub/inner.go", "package sub\n")
	mustWrite(t, root, "sub/deeper/leaf.go", "package deeper\n")
	mustWrite(t, root, "node_modules/left-pad/index.js", "module.exports=1\n")
	mustWrite(t, root, ".hidden/secret.txt", "nope\n")
	mustWrite(t, root, ".git/config", "[core]\n")

	s := New(Config{})
	nodes, err := s.Tree(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("top level = %+v, want main.go and sub", nodes)
	}
	// Directories sort first.
	if !nodes[0].IsDir || nodes[0].Name != "sub" {
		t.Errorf("first node = %+v, want dir sub", nodes[0])
	}
	if nodes[0].Truncated != true {
		t.Errorf("depth-1 dir should be marked truncated (not expanded)")
	}
	if nodes[0].Children != nil {
		t.Errorf("depth-1 dir should not have children loaded")
	}
	if nodes[1].Name != "main.go" || nodes[1].IsDir || nodes[1].Size != int64(len("package main\n")) {
		t.Errorf("file node = %+v", nodes[1])
	}
	if nodes[1].Modified.IsZero() {
		t.Errorf("file node missing modified time")
	}

	// depth=2 expands one level.
	nodes, err = s.Tree(ctx, root, "", 2)
	if err != nil {
		t.Fatalf("tree depth 2: %v", err)
	}
	sub := findNode(nodes, "sub")
	if sub == nil || len(sub.Children) != 2 {
		t.Fatalf("sub children = %+v", sub)
	}
	if sub.Truncated {
		t.Errorf("fully expanded dir should not be truncated")
	}
	deeper := findNode(sub.Children, "deeper")
	if deeper == nil || !deeper.Truncated {
		t.Errorf("deeper should be truncated at depth 2: %+v", deeper)
	}

	// Nested listing.
	nodes, err = s.Tree(ctx, root, "sub", 1)
	if err != nil {
		t.Fatalf("tree sub: %v", err)
	}
	inner := findNode(nodes, "inner.go")
	if inner == nil || inner.Path != "sub/inner.go" {
		t.Errorf("nested node = %+v", inner)
	}

	// Showing ignored files reveals dot directories and node_modules.
	all := New(Config{ShowIgnored: true})
	nodes, err = all.Tree(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("tree showIgnored: %v", err)
	}
	if findNode(nodes, ".hidden") == nil || findNode(nodes, "node_modules") == nil {
		t.Errorf("ShowIgnored tree = %+v", nodes)
	}
	if findNode(nodes, ".git") != nil {
		t.Errorf(".git must never be listed")
	}
}

func TestTreeTruncationAndGitState(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		mustWrite(t, root, name, "x\n")
	}
	s := New(Config{
		MaxEntriesPerDir: 2,
		GitState: func(ctx context.Context, root string) (map[string]string, error) {
			return map[string]string{"a.txt": "M", "b.txt": "U"}, nil
		},
	})
	page, err := s.TreeWithMeta(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(page.Nodes) != 2 {
		t.Fatalf("entries = %d, want 2", len(page.Nodes))
	}
	if !page.Truncated {
		t.Errorf("expected truncation flag on the page")
	}
	if page.Nodes[0].Name != "a.txt" || page.Nodes[0].GitState != "M" {
		t.Errorf("git state not applied: %+v", page.Nodes[0])
	}
	if page.Nodes[1].GitState != "U" {
		t.Errorf("second node = %+v", page.Nodes[1])
	}

	// A failing GitState hook degrades to empty letters, never to an error.
	broken := New(Config{GitState: func(context.Context, string) (map[string]string, error) {
		return nil, errors.New("boom")
	}})
	nodes, err := broken.Tree(ctx, root, "", 1)
	if err != nil || len(nodes) != 4 {
		t.Fatalf("broken hook: %v %d", err, len(nodes))
	}
	for _, n := range nodes {
		if n.GitState != "" {
			t.Errorf("expected empty git state, got %+v", n)
		}
	}
}

func TestTreeRespectsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	ctx := context.Background()
	mustWrite(t, root, ".gitignore", "*.log\nbuild/\n")
	mustWrite(t, root, "keep.go", "package keep\n")
	mustWrite(t, root, "noise.log", "ignored\n")
	mustWrite(t, root, "build/out.bin", "ignored\n")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}

	s := New(Config{})
	nodes, err := s.Tree(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if findNode(nodes, "noise.log") != nil {
		t.Errorf("*.log should be ignored: %+v", nodes)
	}
	if findNode(nodes, "build") != nil {
		t.Errorf("build/ should be ignored: %+v", nodes)
	}
	if findNode(nodes, "keep.go") == nil {
		t.Errorf("keep.go missing: %+v", nodes)
	}

	shown := New(Config{ShowIgnored: true})
	nodes, err = shown.Tree(ctx, root, "", 1)
	if err != nil {
		t.Fatalf("tree showIgnored: %v", err)
	}
	if findNode(nodes, "noise.log") == nil || findNode(nodes, "build") == nil {
		t.Errorf("ShowIgnored should list ignored paths: %+v", nodes)
	}
}

func TestTreeInLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// A linked worktree has a .git FILE pointing at the parent repo, which is
	// the exact root the HTTP layer passes to Tree.
	repo := t.TempDir()
	mustWrite(t, repo, ".gitignore", "*.tmp\n")
	mustWrite(t, repo, "main.go", "package main\n")
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	wt := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-q", "-b", "session/one", wt)
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf(".git should be a file in a linked worktree: %v %v", fi, err)
	}

	ctx := context.Background()
	mustWrite(t, wt, "scratch.tmp", "ignored\n")
	mustWrite(t, wt, "feature.go", "package feature\n")

	s := New(Config{})
	nodes, err := s.Tree(ctx, wt, "", 1)
	if err != nil {
		t.Fatalf("tree in worktree: %v", err)
	}
	if findNode(nodes, "scratch.tmp") != nil {
		t.Errorf("gitignore from the shared repo was not applied: %+v", nodes)
	}
	if findNode(nodes, "feature.go") == nil || findNode(nodes, "main.go") == nil {
		t.Errorf("worktree files missing: %+v", nodes)
	}
	// Reads and writes work against the worktree root too.
	if _, err := s.ReadFile(ctx, wt, "feature.go"); err != nil {
		t.Errorf("read in worktree: %v", err)
	}
	if err := s.WriteFile(ctx, wt, "sub/new.go", []byte("package sub\n")); err != nil {
		t.Errorf("write in worktree: %v", err)
	}
	if _, err := s.ReadFile(ctx, wt, "../../etc/passwd"); err == nil {
		t.Errorf("traversal from a worktree root was allowed")
	}
}

// runGit runs git in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func TestGitignoreFallbackMatcher(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, ".gitignore", "*.tmp\n/root-only.txt\n")
	rules := loadGitignore(root)
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	if !rules.match("a.tmp") || !rules.match("deep/b.tmp") {
		t.Errorf("glob pattern should match anywhere")
	}
	if !rules.match("root-only.txt") {
		t.Errorf("anchored pattern should match at root")
	}
	if rules.match("keep.txt") {
		t.Errorf("unrelated path matched")
	}
}

func TestReadFileSizes(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	mustWrite(t, root, "small.go", "package main\n")
	mustWrite(t, root, "img.png", "PNG\x00\x01\x02binary")
	big := strings.Repeat("abcdefghij\n", 20) // 220 bytes
	mustWrite(t, root, "big.txt", big)
	huge := strings.Repeat("x", 500)
	mustWrite(t, root, "huge.txt", huge)

	s := New(Config{MaxInlineBytes: 100, MaxPreviewBytes: 400, HeadBytes: 30, TailBytes: 30})

	small, err := s.ReadFile(ctx, root, "small.go")
	if err != nil {
		t.Fatalf("small: %v", err)
	}
	if small.Body != "package main\n" || small.Truncated || small.Binary {
		t.Errorf("small = %+v", small)
	}
	if small.Language != "go" {
		t.Errorf("language = %q, want go", small.Language)
	}
	if !strings.Contains(small.MimeType, "go") && !strings.Contains(small.MimeType, "text") {
		t.Errorf("mime = %q", small.MimeType)
	}
	if small.Size != int64(len("package main\n")) || small.Modified.IsZero() {
		t.Errorf("metadata = %+v", small)
	}

	bin, err := s.ReadFile(ctx, root, "img.png")
	if err != nil {
		t.Fatalf("binary: %v", err)
	}
	if !bin.Binary || bin.Body != "" {
		t.Errorf("binary = %+v", bin)
	}
	if bin.MimeType == "" {
		t.Errorf("binary mime must be set")
	}

	mid, err := s.ReadFile(ctx, root, "big.txt")
	if err != nil {
		t.Fatalf("mid: %v", err)
	}
	if !mid.Truncated || mid.Binary {
		t.Errorf("mid = %+v", mid)
	}
	if !strings.Contains(mid.Body, "abcdefghij") {
		t.Errorf("head missing: %q", mid.Body)
	}
	if !strings.Contains(mid.Body, "… (truncated) …") {
		t.Errorf("truncation marker missing: %q", mid.Body)
	}

	meta, err := s.ReadFile(ctx, root, "huge.txt")
	if err != nil {
		t.Fatalf("huge: %v", err)
	}
	if !meta.Truncated || meta.Body != "" {
		t.Errorf("huge = %+v", meta)
	}
	if meta.Size != 500 {
		t.Errorf("size = %d", meta.Size)
	}
}

func TestOpenFileSkipsTextDecoding(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0xfe}
	if err := os.WriteFile(filepath.Join(root, "icon.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{})

	body, size, err := s.OpenFile(ctx, root, "icon.png")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, png) || size != int64(len(png)) {
		t.Fatalf("stream = %x size=%d, want %x size=%d", got, size, png, len(png))
	}

	if _, _, err := s.OpenFile(ctx, root, "nope.png"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if _, _, err := s.OpenFile(ctx, root, "."); !errors.Is(err, ErrIsDir) {
		t.Fatalf("directory error = %v", err)
	}
}

func TestWriteDeleteAudit(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	var events []AuditEvent
	s := New(Config{Audit: func(ev AuditEvent) { events = append(events, ev) }})

	if err := s.WriteFile(ctx, root, "dir/new.txt", []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "dir", "new.txt")); err != nil || string(body) != "hello" {
		t.Fatalf("file on disk = %q, %v", body, err)
	}
	if err := s.DeleteFile(ctx, root, "dir/new.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("audit events = %+v", events)
	}
	if events[0].Action != "write" || events[0].Path != "dir/new.txt" || events[0].Size != 5 {
		t.Errorf("write event = %+v", events[0])
	}
	if events[1].Action != "delete" || events[1].Path != "dir/new.txt" {
		t.Errorf("delete event = %+v", events[1])
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("file still present")
	}

	// Deleting a directory tree works, deleting the root does not.
	if err := s.DeleteFile(ctx, root, "dir"); err != nil {
		t.Errorf("delete dir: %v", err)
	}
	if err := s.DeleteFile(ctx, root, "."); err == nil {
		t.Errorf("deleting the root must be refused")
	}
	if err := s.WriteFile(ctx, root, ".git/hooks/pre-commit", []byte("evil")); err == nil {
		t.Errorf("writing into .git must be refused")
	}
}

func TestSearchContentAndNames(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	mustWrite(t, root, "alpha.go", "package alpha\n// piforge marker\n")
	mustWrite(t, root, "beta.md", "nothing here\n")
	mustWrite(t, root, "src/gamma.go", "package gamma\n// piforge marker\n")
	mustWrite(t, root, ".git/config", "piforge marker\n")

	s := New(Config{})
	content, err := s.Search(ctx, root, "piforge marker", true)
	if err != nil {
		t.Fatalf("search content: %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("content hits = %+v, want 2", content)
	}
	for _, hit := range content {
		if strings.HasPrefix(hit.Path, ".git") {
			t.Errorf("search leaked git metadata: %+v", hit)
		}
		if hit.Line != 2 || !strings.Contains(hit.Text, "piforge") {
			t.Errorf("hit = %+v", hit)
		}
	}

	names, err := s.Search(ctx, root, "gamma", false)
	if err != nil {
		t.Fatalf("search names: %v", err)
	}
	if len(names) != 1 || names[0].Path != "src/gamma.go" {
		t.Errorf("name hits = %+v", names)
	}

	// A regex that only works literally falls back to fixed strings.
	literal, err := s.Search(ctx, root, "piforge (marker", true)
	if err != nil {
		t.Fatalf("literal search: %v", err)
	}
	if len(literal) != 0 {
		t.Errorf("unexpected literal hits: %+v", literal)
	}

	if _, err := s.Search(ctx, root, "  ", true); err == nil {
		t.Errorf("empty query should fail")
	}
}

func TestWalkSearchFallback(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	mustWrite(t, root, "a.txt", "needle here\n")
	mustWrite(t, root, "node_modules/b.txt", "needle here\n")
	mustWrite(t, root, "sub/c.txt", "nothing\n")

	s := New(Config{SearchLimit: 10})
	hits, err := s.walkSearch(ctx, root, "needle", true)
	if err != nil {
		t.Fatalf("walk search: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "a.txt" || hits[0].Line != 1 {
		t.Fatalf("walk hits = %+v", hits)
	}
	names, err := s.walkSearch(ctx, root, "c.txt", false)
	if err != nil {
		t.Fatalf("walk name search: %v", err)
	}
	if len(names) != 1 || names[0].Path != "sub/c.txt" {
		t.Errorf("walk name hits = %+v", names)
	}

	// Cancelled contexts are reported, not silently ignored.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.walkSearch(cancelled, root, "needle", true); err == nil {
		t.Errorf("expected a cancellation error")
	}
}

func TestServiceSatisfiesWorkspace(t *testing.T) {
	// Compile-time assertion lives in fsx.go; this proves the delegation path
	// fails loudly instead of panicking when no backend is injected.
	s := New(Config{})
	if _, err := s.Status(context.Background(), t.TempDir()); !errors.Is(err, ErrNoGitBackend) {
		t.Errorf("status without backend = %v", err)
	}
	if err := s.Stage(context.Background(), t.TempDir(), nil); !errors.Is(err, ErrNoGitBackend) {
		t.Errorf("stage without backend = %v", err)
	}
}

func TestReadFileMissing(t *testing.T) {
	s := New(Config{})
	if _, err := s.ReadFile(context.Background(), t.TempDir(), "nope.txt"); !errors.Is(err, ErrNotExist) {
		t.Errorf("missing file err = %v", err)
	}
	root := t.TempDir()
	if _, err := s.ReadFile(context.Background(), root, "."); !errors.Is(err, ErrIsDir) {
		t.Errorf("dir read err = %v", err)
	}
}

func TestTreeContextCancel(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "a.txt", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).Tree(ctx, root, "", 2); err == nil {
		t.Errorf("cancelled tree should fail")
	}
}
