package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// writeSessionFile lays down a Pi session file: a header plus the JSONL entry
// lines given, exactly as Pi writes them.
func writeSessionFile(t *testing.T, dir, cwd string, entries ...string) string {
	t.Helper()
	path := filepath.Join(dir, "ses_test.jsonl")
	header, err := json.Marshal(map[string]any{
		"type": "session", "version": 3, "id": "01a18770-144c-730e-ad71-499d2e92b007",
		"timestamp": "2026-10-29T11:33:51.820Z", "cwd": cwd,
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	body := string(header) + "\n" + strings.Join(entries, "\n")
	if len(entries) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	return path
}

// headerCwd reads the cwd Pi would take the session's working directory from.
func headerCwd(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	first, _, _ := strings.Cut(string(raw), "\n")
	var header struct {
		Type string `json:"type"`
		Cwd  string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(first), &header); err != nil {
		t.Fatalf("decode header %q: %v", first, err)
	}
	if header.Type != "session" {
		t.Fatalf("first line is not a session header: %q", first)
	}
	return header.Cwd
}

// TestAlignSessionCwdMovesTheAgentToTheWorktree is the blocker in one test: Pi
// runs its tools in the directory stored in the session file, so that directory
// is what a forked or cloned session has to be corrected on.
func TestAlignSessionCwdMovesTheAgentToTheWorktree(t *testing.T) {
	dir := t.TempDir()
	worktree := filepath.Join(dir, "worktrees", "branch-a")
	entries := []string{
		`{"type":"model_change","id":"m1","parentId":null}`,
		`{"type":"message","id":"m2","parentId":"m1","message":{"role":"user","content":"hi"}}`,
	}
	path := writeSessionFile(t, dir, filepath.Join(dir, "repo"), entries...)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	changed, err := alignSessionCwd(path, worktree)
	if err != nil {
		t.Fatalf("alignSessionCwd: %v", err)
	}
	if !changed {
		t.Fatal("alignSessionCwd reported no change")
	}
	if got := headerCwd(t, path); got != worktree {
		t.Fatalf("header cwd = %q, want %q", got, worktree)
	}

	// The conversation below the header must survive byte for byte: re-encoding
	// it would risk changing what the model reads.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	_, restBefore, _ := strings.Cut(string(before), "\n")
	_, restAfter, _ := strings.Cut(string(after), "\n")
	if restBefore != restAfter {
		t.Fatalf("entries changed:\nbefore=%q\nafter=%q", restBefore, restAfter)
	}

	// Idempotent: a correct file is left alone, which is what makes this safe to
	// run on every spawn (a resume must not rewrite the file each time).
	changed, err = alignSessionCwd(path, worktree)
	if err != nil {
		t.Fatalf("second alignSessionCwd: %v", err)
	}
	if changed {
		t.Fatal("a file already pointing at the worktree was rewritten")
	}
}

// A session that has not been written yet, and a file that is not a session
// header, must both be left alone rather than turned into something Pi cannot
// open.
func TestAlignSessionCwdIgnoresFilesItDoesNotOwn(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.jsonl")
	if changed, err := alignSessionCwd(missing, filepath.Join(dir, "wt")); err != nil || changed {
		t.Fatalf("missing file: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("alignSessionCwd created a session file")
	}

	foreign := filepath.Join(dir, "foreign.jsonl")
	if err := os.WriteFile(foreign, []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if changed, err := alignSessionCwd(foreign, filepath.Join(dir, "wt")); err != nil || changed {
		t.Fatalf("foreign file: changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(foreign)
	if string(raw) != "not json\n" {
		t.Fatalf("foreign file was rewritten: %q", raw)
	}
}

// TestSpawnAlignsTheForkedSessionsCwd is the spawn-level contract: whatever the
// session file said when it was handed to the runtime (a fork inherits its
// parent's repo), the process that opens it starts in the session's own worktree.
func TestSpawnAlignsTheForkedSessionsCwd(t *testing.T) {
	repoDir := t.TempDir()
	branchDir := t.TempDir()
	rt := newFakeRuntime(t, "ok", 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	// A branch session whose file still says the parent's checkout, which is
	// exactly what Pi's fork and the byte-for-byte clone produce.
	repo := filepath.Join(repoDir, "repo")
	branch := filepath.Join(branchDir, "worktrees", "qa14-fork")
	for _, dir := range []string{repo, branch} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	spec := contracts.SessionSpec{
		SessionID: "ses_fork",
		Kind:      contracts.KindBranch,
		Worktree:  branch,
	}
	target := rt.sessionFileFor(spec.SessionID)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeSessionFile(t, filepath.Dir(target), repo, `{"type":"message","id":"m1"}`)
	// writeSessionFile names the file ses_test.jsonl; move it into place so the
	// runtime opens this exact file.
	if err := os.Rename(filepath.Join(filepath.Dir(target), "ses_test.jsonl"), target); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := headerCwd(t, target); got != repo {
		t.Fatalf("fixture header cwd = %q, want %q", got, repo)
	}

	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := headerCwd(t, target); got != branch {
		t.Fatalf("the agent's session cwd = %q, want the branch worktree %q", got, branch)
	}
}

// TestStartedModelRefIsTheSpawnedReference: the picker compares the session row
// against this, so it must keep reporting what the process was launched with even
// after the row — and the spec handed to Start — moved on.
func TestStartedModelRefIsTheSpawnedReference(t *testing.T) {
	rt := newFakeRuntime(t, "ok", 0)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_model", contracts.KindMain)
	spec.ModelRef = "volc/deepseek-v4-flash"
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	started, live := rt.StartedModelRef("ses_model")
	if !live {
		t.Fatal("StartedModelRef reports no live process after Start")
	}
	if started != "volc/deepseek-v4-flash" {
		t.Fatalf("started ref = %q", started)
	}

	// A later Start carrying the new row must not relabel the running process.
	spec.ModelRef = "volc/other-model"
	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start again: %v", err)
	}
	started, _ = rt.StartedModelRef("ses_model")
	if started != "volc/deepseek-v4-flash" {
		t.Fatalf("started ref after a spec refresh = %q, want the spawned one", started)
	}

	if _, live := rt.StartedModelRef("ses_absent"); live {
		t.Fatal("StartedModelRef reported a live process for an unknown session")
	}
}
