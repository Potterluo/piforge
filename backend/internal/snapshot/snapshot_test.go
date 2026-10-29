package snapshot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/store"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

type fixture struct {
	st  *store.Store
	sb  *contracts.Sandbox
	dir string
}

func newFixture(t *testing.T, root string, state contracts.SandboxState) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenWithNow(ctx, ":memory:", func() time.Time {
		return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("store.OpenWithNow: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, err := st.CreateUser(ctx, store.NewUser{
		Email: "snapshot@piforge.local", DisplayName: "snapshot", PasswordHash: "hash",
		Role: contracts.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "snap", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sb, err := st.CreateSandbox(ctx, user.ID, store.NewSandbox{
		ProjectID: project.ID, State: state, Provider: "local", RootDir: root,
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	return &fixture{st: st, sb: sb, dir: root}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTakeIndexesFilesGitAndUncommittedWork(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "sandboxes", "user-1", "project-1")
	repoDir := filepath.Join(root, "repo")

	git := gitx.New(gitx.Config{})
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := git.Init(ctx, repoDir, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "README.md"), "hello\n")
	writeFile(t, filepath.Join(repoDir, "src", "main.go"), "package main\n")
	if err := git.Stage(ctx, repoDir, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	head, err := git.Commit(ctx, repoDir, contracts.CommitRequest{Message: "initial"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	wtDir := filepath.Join(root, "worktrees", "fix-cache")
	if err := git.AddWorktree(ctx, repoDir, wtDir, "fix-cache", true); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// Uncommitted work in both checkouts is exactly what a manifest must name.
	writeFile(t, filepath.Join(repoDir, "README.md"), "hello\nchanged\n")
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "scratch\n")
	writeFile(t, filepath.Join(wtDir, "wt-change.txt"), "work in progress\n")

	f := newFixture(t, root, contracts.SandboxReady)
	writer, err := New(Config{
		Root:  filepath.Join(base, "snapshots"),
		Store: f.st,
		Git:   git,
		Now: func() time.Time {
			return time.Date(2026, 3, 1, 12, 34, 56, 123000000, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	row, err := writer.Take(ctx, f.sb)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if row.Kind != "fs" {
		t.Errorf("kind = %q, want fs", row.Kind)
	}
	if row.GitCommit != head {
		t.Errorf("git_commit = %q, want %q", row.GitCommit, head)
	}
	if len(row.ContentHash) != 64 {
		t.Errorf("content_hash = %q, want 64 hex chars", row.ContentHash)
	}
	// The documented layout: <dataDir>/snapshots/<sandbox-id>/<timestamp>/
	if !strings.HasPrefix(row.BlobKey, f.sb.ID+"/") || !strings.HasSuffix(row.BlobKey, "/manifest.json") {
		t.Errorf("blob_key = %q", row.BlobKey)
	}
	if strings.Contains(row.BlobKey, ":") {
		t.Errorf("blob_key must be Windows-safe (no colon): %q", row.BlobKey)
	}
	manifestPath := filepath.Join(writer.Root(), filepath.FromSlash(row.BlobKey))
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var man Manifest
	if err := json.Unmarshal(body, &man); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if man.SandboxID != f.sb.ID || man.UserID != f.sb.UserID || man.ProjectID != f.sb.ProjectID {
		t.Errorf("manifest identity = %+v", man)
	}
	if len(man.Checkouts) != 2 {
		t.Fatalf("checkouts = %d, want repo + one worktree", len(man.Checkouts))
	}

	byRel := map[string]Checkout{}
	for _, c := range man.Checkouts {
		byRel[c.RelPath] = c
	}
	main, ok := byRel["repo"]
	if !ok {
		t.Fatalf("main checkout missing: %+v", man.Checkouts)
	}
	if main.Kind != "main" || main.Branch != "main" || main.Head != head {
		t.Errorf("main checkout = %+v", main)
	}
	if !containsFile(main.Files, "README.md") || !containsFile(main.Files, "src/main.go") {
		t.Errorf("main files = %+v", main.Files)
	}
	if !contains(main.Uncommitted, "README.md") || !contains(main.Uncommitted, "notes.txt") {
		t.Errorf("main uncommitted = %+v", main.Uncommitted)
	}

	wt, ok := byRel["worktrees/fix-cache"]
	if !ok {
		t.Fatalf("worktree checkout missing: %+v", man.Checkouts)
	}
	if wt.Kind != "worktree" || wt.Branch != "fix-cache" || wt.Head != head {
		t.Errorf("worktree checkout = %+v", wt)
	}
	if !contains(wt.Uncommitted, "wt-change.txt") {
		t.Errorf("worktree uncommitted = %+v", wt.Uncommitted)
	}
	// The manifest must never index git's own bookkeeping as workspace files.
	for _, c := range man.Checkouts {
		for _, file := range c.Files {
			if file.Path == ".git" || strings.HasPrefix(file.Path, ".git/") {
				t.Errorf("%s indexed %q", c.RelPath, file.Path)
			}
		}
	}
	if man.FileCount < 4 || man.Bytes <= 0 {
		t.Errorf("manifest totals = %d files / %d bytes", man.FileCount, man.Bytes)
	}
	if man.Truncated {
		t.Errorf("unexpected truncation: %+v", man)
	}

	rows, err := f.st.ListSnapshots(ctx, f.sb.UserID, f.sb.ID)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != row.ID {
		t.Errorf("store rows = %+v", rows)
	}
}

func TestTakeSucceedsWithoutGitButRecordsTheError(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "sandboxes", "user-1", "project-1")
	writeFile(t, filepath.Join(root, "repo", "paper.txt"), "no git here\n")

	f := newFixture(t, root, contracts.SandboxReady)
	writer, err := New(Config{Root: filepath.Join(base, "snapshots"), Store: f.st})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	row, err := writer.Take(ctx, f.sb)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(writer.Root(), filepath.FromSlash(row.BlobKey)))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var man Manifest
	if err := json.Unmarshal(body, &man); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(man.Checkouts) != 1 || man.Checkouts[0].GitError == "" {
		t.Fatalf("checkout = %+v, want a recorded git error", man.Checkouts)
	}
	if !containsFile(man.Checkouts[0].Files, "paper.txt") {
		t.Errorf("files = %+v", man.Checkouts[0].Files)
	}
}

func TestTakeFailsWhenWorkspaceIsGone(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "sandboxes", "user-1", "project-1")
	f := newFixture(t, root, contracts.SandboxPaused)
	writer, err := New(Config{Root: filepath.Join(base, "snapshots"), Store: f.st})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := writer.Take(ctx, f.sb); err == nil {
		t.Fatal("Take on a missing workspace must fail; reclaim may not proceed")
	}
}

func TestTakeCapsTheFileIndex(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "sandboxes", "user-1", "project-1")
	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(root, "repo", "file-"+string(rune('a'+i))+".txt"), "x")
	}
	f := newFixture(t, root, contracts.SandboxPaused)
	writer, err := New(Config{Root: filepath.Join(base, "snapshots"), Store: f.st, MaxFiles: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	row, err := writer.Take(ctx, f.sb)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(writer.Root(), filepath.FromSlash(row.BlobKey)))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var man Manifest
	if err := json.Unmarshal(body, &man); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if !man.Truncated {
		t.Errorf("manifest not marked truncated: %+v", man)
	}
	if man.FileCount > 3 {
		t.Errorf("file_count = %d, want <= 3", man.FileCount)
	}
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func containsFile(files []File, want string) bool {
	for _, f := range files {
		if f.Path == want {
			return true
		}
	}
	return false
}
