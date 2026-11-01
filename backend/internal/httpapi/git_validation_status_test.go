package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// G7 of docs/DATA-FLOW-AUDIT.md: the guard held but the status lied.
//
// `POST /git/branch {"name":"-D"}` answered 500 with the guard's own sentence
// ("revision "-D" may not start with '-'"), which reads as "the server is
// broken" for a request that was refused because the client wrote something
// git cannot use. A validation refusal is a 400 on every git route: branch,
// checkout, commit, diff (commit, branch base and range) and log, plus the
// worktree provisioning path that has no route of its own.
//
// The test produces each refusal with the real gitx code against a real
// repository (not a hand-written string that could drift from the guard), hands
// it to the route through the workspace, and asserts the status the HTTP layer
// picks. Nothing here asserts a message: the sentence belongs to gitx.

// g7Repo is one real repository with a commit, so the refusals are gitx's own.
func g7Repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitIn(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitIn(t, dir, "add", "-A")
	runGitIn(t, dir, "commit", "-m", "seed")
	return dir
}

// TestGitValidationRefusalsAre400 is the table the GAP asked for: one row per
// edge a tenant revision reaches, the real refusal gitx produces for it, and
// the status the route answers.
func TestGitValidationRefusalsAre400(t *testing.T) {
	repo := g7Repo(t)
	runner := gitx.NewRunner()
	ctx := context.Background()

	// Produce every refusal once, at its own site.
	branchErr := runner.CreateBranch(ctx, repo, "-D")
	checkoutErr := runner.Checkout(ctx, repo, "../../x")
	_, commitErr := runner.Commit(ctx, repo, contracts.CommitRequest{Message: "m", Paths: []string{"../x"}})
	_, rangeErr := runner.Diff(ctx, repo, contracts.DiffRequest{Scope: contracts.ScopeRange, From: "-D"})
	_, baseErr := runner.Diff(ctx, repo, contracts.DiffRequest{Scope: contracts.ScopeBranch, Base: "-D"})
	_, commitDiffErr := runner.Diff(ctx, repo, contracts.DiffRequest{Scope: contracts.ScopeCommit, Commit: "-D"})
	_, cursorErr := runner.Log(ctx, repo, 10, "not-a-number")
	worktreeErr := runner.AddWorktree(ctx, repo, filepath.Join(t.TempDir(), "wt"), "-D", true)

	refusals := []struct {
		name string
		err  error
	}{
		{"branch", branchErr},
		{"checkout", checkoutErr},
		{"commit paths", commitErr},
		{"diff range", rangeErr},
		{"diff branch base", baseErr},
		{"diff commit", commitDiffErr},
		{"log cursor", cursorErr},
		{"worktree branch", worktreeErr},
	}
	for _, r := range refusals {
		if r.err == nil {
			t.Fatalf("%s: the mutation guard did not refuse, so this test proves nothing", r.name)
		}
		if !errors.Is(r.err, gitx.ErrInvalidRequest) && !errors.Is(r.err, gitx.ErrPathEscape) {
			t.Fatalf("%s: %v is not a typed gitx validation refusal", r.name, r.err)
		}
	}

	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "g7")
	env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	// The routes. `worktree` has no HTTP route (it is provisioning), so its row
	// is the classify assertion above: whatever maps a gitx refusal to a status
	// is the same function the routes use.
	routes := []struct {
		name   string
		method string
		path   string
		body   any
		cause  error
	}{
		{"branch", http.MethodPost, base + "/git/branch", map[string]any{"name": "-D"}, branchErr},
		{"checkout", http.MethodPost, base + "/git/checkout", map[string]any{"name": "../../x"}, checkoutErr},
		{"commit", http.MethodPost, base + "/git/commit",
			map[string]any{"message": "m", "paths": []string{"../x"}}, commitErr},
		{"diff range", http.MethodGet, base + "/git/diff?scope=range&from=-D", nil, rangeErr},
		{"diff branch base", http.MethodGet, base + "/git/diff?scope=branch&base=-D", nil, baseErr},
		{"diff commit", http.MethodGet, base + "/git/diff?scope=commit&commit=-D", nil, commitDiffErr},
		{"log cursor", http.MethodGet, base + "/git/log?cursor=not-a-number", nil, cursorErr},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			env.workspace.err = route.cause
			rec := env.doAuthed(route.method, route.path, route.body, c)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want 400: %s", route.method, route.path, rec.Code, rec.Body.String())
			}
			env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
		})
	}

	// Every refusal, wherever it surfaces, classifies as a 400. This is the row
	// for `worktree` (provisioning has no route) and the belt for the rest.
	for _, r := range refusals {
		if status, _ := classify(r.err); status != http.StatusBadRequest {
			t.Fatalf("%s: classify = %d, want 400 (error: %v)", r.name, status, r.err)
		}
	}
}
