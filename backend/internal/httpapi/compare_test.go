package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/store"
)

// FR-2.5: two or three branches side by side. The screen exists so a person can pick
// one, so the numbers have to be real — and the boundary cases have to be refused
// rather than rendered as zeros.
func TestComparePutsBranchesSideBySide(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, main := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, main.ID)

	other, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, RootID: main.ID, ParentID: main.ID,
		Title: "second attempt",
	})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID+","+other.ID, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("compare: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Rows []struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
			Kind      string `json:"kind"`
			DiffError string `json:"diff_error"`
		} `json:"rows"`
	}
	env.mustJSON(rec, &body)
	if len(body.Rows) != 2 {
		t.Fatalf("rows = %d", len(body.Rows))
	}
	if body.Rows[0].SessionID != main.ID || body.Rows[1].SessionID != other.ID {
		t.Fatalf("order = %+v", body.Rows)
	}
	// The branch has no checkout of its own here, and the screen says so instead of
	// reporting a confident "0 files changed".
	if body.Rows[1].DiffError == "" {
		t.Fatalf("a session without a checkout reported a change size: %+v", body.Rows[1])
	}

	// Boundaries: one id is not a comparison, four is too many, the same id twice
	// is not a comparison either, and a session that is not yours must not appear.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID, nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("single id = %d", rec.Code)
	}
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID+","+main.ID, nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate id = %d %s", rec.Code, rec.Body.String())
	}
	env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	stranger := env.login("other@piforge.local", "pw2")
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID+","+other.ID, nil, stranger); rec.Code != http.StatusNotFound {
		t.Fatalf("someone else's sessions = %d", rec.Code)
	}
}

// compareRowBody is the wire shape this file asserts on.
type compareRowBody struct {
	SessionID            string `json:"session_id"`
	UserMessages         int    `json:"user_messages"`
	TokensIn             int64  `json:"tokens_in"`
	TokensOut            int64  `json:"tokens_out"`
	InheritedUserMessage int    `json:"inherited_user_messages"`
	InheritedTokensIn    int64  `json:"inherited_tokens_in"`
	InheritedTokensOut   int64  `json:"inherited_tokens_out"`
	FilesChanged         int    `json:"files_changed"`
	Additions            int    `json:"additions"`
	Deletions            int    `json:"deletions"`
	CommittedFiles       int    `json:"committed_files"`
	CommittedAdditions   int    `json:"committed_additions"`
	UncommittedFiles     int    `json:"uncommitted_files"`
	UncommittedAdditions int    `json:"uncommitted_additions"`
	DiffError            string `json:"diff_error"`
}

func compareRows(t *testing.T, env *testEnv, c *creds, ids ...string) []compareRowBody {
	t.Helper()
	path := "/v1/sessions/compare?ids=" + ids[0]
	for _, id := range ids[1:] {
		path += "," + id
	}
	rec := env.doAuthed(http.MethodGet, path, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("compare: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Rows []compareRowBody `json:"rows"`
	}
	env.mustJSON(rec, &body)
	if len(body.Rows) != len(ids) {
		t.Fatalf("rows = %d, want %d", len(body.Rows), len(ids))
	}
	return body.Rows
}

// The bug this covers: the compare screen only looked at staged/unstaged work, so a
// branch whose agent committed its work showed "0 个文件 / +0 -0" — the one number a
// person uses to pick a branch. It must count the branch's commits since the merge
// base *and* whatever is still uncommitted, and an untouched branch must still be 0.
func TestCompareCountsCommittedAndUncommittedWork(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, main := env.mkSessionFor(user.ID, "repo")

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	branchOne := filepath.Join(base, "attempt-one")
	branchTwo := filepath.Join(base, "attempt-two")
	ctx := t.Context()

	runner := gitx.NewRunner()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runner.Init(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stage(ctx, repo, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Commit(ctx, repo, contracts.CommitRequest{Message: "init"}); err != nil {
		t.Fatal(err)
	}
	for _, wt := range []struct{ path, branch string }{
		{branchOne, "attempt-one"},
		{branchTwo, "attempt-two"},
	} {
		if err := runner.AddWorktree(ctx, repo, wt.path, wt.branch, true); err != nil {
			t.Fatalf("add worktree %s: %v", wt.branch, err)
		}
	}

	// The handler must ask the real workspace, not the canned fake.
	env.deps.Workspace = fsx.New(fsx.Config{Git: runner})
	env.rebuild()

	// One sandbox per project, the same granularity the product uses.
	sandbox, err := env.store.CreateSandbox(ctx, user.ID, store.NewSandbox{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	attachCompareWorktree(t, env, user.ID, project.ID, main.ID, sandbox.ID, repo, "main")
	one := attachBranchSession(t, env, user.ID, project.ID, main.ID, sandbox.ID, "attempt one", branchOne, "attempt-one")
	two := attachBranchSession(t, env, user.ID, project.ID, main.ID, sandbox.ID, "attempt two", branchTwo, "attempt-two")

	// A commit on the first branch: this is the half that used to be invisible.
	writeAndCommit(t, env, c, one, "committed.txt", "one\ntwo\n", "the agent committed this")
	// Plus one uncommitted file, so the row has to report both halves.
	writeFile(t, env, c, one, "still-dirty.txt", "not committed\n")

	rows := compareRows(t, env, c, main.ID, one, two)
	bySession := map[string]compareRowBody{}
	for _, row := range rows {
		bySession[row.SessionID] = row
	}

	first := bySession[one]
	if first.DiffError != "" {
		t.Fatalf("branch one has a diff error: %q", first.DiffError)
	}
	if first.CommittedFiles != 1 || first.CommittedAdditions != 2 {
		t.Fatalf("committed part = %d files / +%d, want 1 / +2 (%+v)",
			first.CommittedFiles, first.CommittedAdditions, first)
	}
	if first.UncommittedFiles != 1 || first.UncommittedAdditions != 1 {
		t.Fatalf("uncommitted part = %d files / +%d, want 1 / +1 (%+v)",
			first.UncommittedFiles, first.UncommittedAdditions, first)
	}
	if first.FilesChanged != 2 || first.Additions != 3 {
		t.Fatalf("total = %d files / +%d, want 2 / +3 (%+v)", first.FilesChanged, first.Additions, first)
	}

	// An identical branch is still zero: the fix must not invent work.
	second := bySession[two]
	if second.DiffError != "" || second.FilesChanged != 0 || second.Additions != 0 || second.Deletions != 0 {
		t.Fatalf("untouched branch = %+v, want 0 files / +0 -0", second)
	}

	// The trunk column shows the trunk's own uncommitted work, not a branch's.
	trunk := bySession[main.ID]
	if trunk.FilesChanged != 0 || trunk.CommittedFiles != 0 {
		t.Fatalf("trunk row = %+v, want no changes", trunk)
	}
}

// A cloned session copies the transcript but not its runs, so its usage used to
// start at 0 turns / 0 tokens. Compare must count what it inherited.
func TestCompareCountsInheritedUsage(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, source := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, source.ID)

	// One turn with a measurable cost on the source session.
	if _, _, err := env.store.SaveMessage(t.Context(), user.ID, source.ID, "cm_1", "first question",
		contracts.PromptResult{Accepted: true, Disposition: "started"}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	run, err := env.store.CreateRun(t.Context(), user.ID, store.NewRun{SessionID: source.ID})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := env.store.SettleRun(t.Context(), user.ID, run.ID, store.RunSettle{
		TokensIn: 120, TokensOut: 45, CostUSD: 0.25,
	}); err != nil {
		t.Fatalf("SettleRun: %v", err)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+source.ID+"/clone", nil, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body.String())
	}
	var cloned struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(rec, &cloned)

	rows := compareRows(t, env, c, source.ID, cloned.Session.ID)
	bySession := map[string]compareRowBody{}
	for _, row := range rows {
		bySession[row.SessionID] = row
	}

	clone := bySession[cloned.Session.ID]
	if clone.InheritedUserMessage != 1 || clone.InheritedTokensIn != 120 || clone.InheritedTokensOut != 45 {
		t.Fatalf("inherited usage = %d turns / %d in / %d out, want 1 / 120 / 45 (%+v)",
			clone.InheritedUserMessage, clone.InheritedTokensIn, clone.InheritedTokensOut, clone)
	}
	if clone.UserMessages != 1 || clone.TokensIn != 120 || clone.TokensOut != 45 {
		t.Fatalf("clone totals = %d turns / %d in / %d out, want the inherited numbers (%+v)",
			clone.UserMessages, clone.TokensIn, clone.TokensOut, clone)
	}

	// The source has no inherited part: only its own usage is counted once.
	origin := bySession[source.ID]
	if origin.InheritedUserMessage != 0 || origin.InheritedTokensIn != 0 {
		t.Fatalf("source row invented inherited usage: %+v", origin)
	}
	if origin.UserMessages != 1 || origin.TokensIn != 120 {
		t.Fatalf("source row = %+v, want its own single turn", origin)
	}
}

// attachCompareWorktree gives a session its checkout without going through a
// provisioner, so the test owns the git repository.
func attachCompareWorktree(t *testing.T, env *testEnv, userID, projectID, sessionID, sandboxID, path, branch string) {
	t.Helper()
	ctx := t.Context()
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sandboxID, Path: path, Branch: branch,
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sessionID, store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}
}

func attachBranchSession(t *testing.T, env *testEnv, userID, projectID, parentID, sandboxID, title, path, branch string) string {
	t.Helper()
	sess, err := env.store.CreateSession(t.Context(), userID, store.NewSession{
		ProjectID: projectID, Kind: contracts.KindBranch, RootID: parentID, ParentID: parentID,
		Label: title, Title: title,
	})
	if err != nil {
		t.Fatalf("create branch %q: %v", title, err)
	}
	attachCompareWorktree(t, env, userID, projectID, sess.ID, sandboxID, path, branch)
	return sess.ID
}

func writeFile(t *testing.T, env *testEnv, c *creds, sessionID, path, body string) {
	t.Helper()
	rec := env.doRaw(http.MethodPut, "/v1/sessions/"+sessionID+"/files?path="+path,
		"text/plain; charset=utf-8", []byte(body), c)
	if rec.Code != http.StatusOK {
		t.Fatalf("write %s: %d %s", path, rec.Code, rec.Body.String())
	}
}

func writeAndCommit(t *testing.T, env *testEnv, c *creds, sessionID, path, body, message string) {
	t.Helper()
	writeFile(t, env, c, sessionID, path, body)
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sessionID+"/git/commit",
		map[string]any{"message": message, "paths": []string{path}}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var committed struct {
		Commit string `json:"commit"`
	}
	env.mustJSON(rec, &committed)
	if committed.Commit == "" {
		t.Fatalf("commit %s returned no id: %s", path, rec.Body.String())
	}
}
