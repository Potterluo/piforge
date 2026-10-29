package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/store"
)

// FR-2.6: the second half of "try three approaches and pick one" — the picking.
func TestAdoptCarriesABranchBackToItsSource(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, parent := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, parent.ID)

	branch, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID,
		Kind:      contracts.KindBranch,
		RootID:    parent.ID,
		ParentID:  parent.ID,
		Label:     "alt",
		Title:     "alt",
	})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	attachSecondWorktree(t, env, user.ID, project.ID, parent.ID, branch.ID)

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+branch.ID+"/adopt",
		map[string]any{"message": "adopt: alt"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Adopted struct {
			Commit      string   `json:"commit"`
			Files       []string `json:"files"`
			Mode        string   `json:"mode"`
			Commits     []string `json:"commits"`
			MergeCommit string   `json:"merge_commit"`
		} `json:"adopted"`
	}
	env.mustJSON(rec, &body)
	if body.Adopted.Commit == "" {
		t.Fatal("no commit reported")
	}
	// The response has to say *how* the work travelled: a committed branch produces
	// commits and a merge/ff mode, not a working-tree patch.
	if body.Adopted.Mode != "merge" || len(body.Adopted.Commits) != 1 || body.Adopted.MergeCommit == "" {
		t.Fatalf("adopted = %+v, want the merge shape", body.Adopted)
	}

	// The hand-off really pointed at the branch's worktree and its source.
	if len(env.workspace.adopted) != 1 {
		t.Fatalf("adopted %v", env.workspace.adopted)
	}
	if !containsAll(env.workspace.adopted[0], "#adopt: alt") {
		t.Fatalf("message did not reach git: %q", env.workspace.adopted[0])
	}

	// A main session has nothing to adopt into; a sidecar has no checkout of its own.
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/adopt", nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("adopting a main session = %d", rec.Code)
	}
}

func TestAdoptExplainsRefusals(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, parent := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, parent.ID)
	branch, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, RootID: parent.ID, ParentID: parent.ID,
	})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	attachSecondWorktree(t, env, user.ID, project.ID, parent.ID, branch.ID)

	// Every refusal is a 409 with an actionable sentence: never a 500, and never a
	// conflict reported as if the target were untouched when the merge half landed.
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"nothing to adopt", gitx.ErrNothingToAdopt, []string{"没有可采纳的改动", "提交", "未提交"}},
		{"target dirty", gitx.ErrTargetDirty, []string{"目标会话的工作区有未提交的改动", "先", "再重试"}},
		{"no common history", gitx.ErrNoCommonBase, []string{"没有共同历史"}},
		{"merge conflict", &gitx.MergeConflictError{Files: []string{"a.txt", "b.txt"}, Stage: "commits"},
			[]string{"a.txt、b.txt", "目标没有改动", "回滚"}},
		{"working-tree conflict", &gitx.MergeConflictError{Files: []string{"c.txt"}, Stage: "working-tree"},
			[]string{"c.txt", "提交已经合并", "没有应用"}},
	} {
		env.workspace.err = tc.err
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+branch.ID+"/adopt", nil, c)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: status = %d", tc.name, rec.Code)
		}
		if !containsAll(rec.Body.String(), tc.want...) {
			t.Fatalf("%s: body = %s", tc.name, rec.Body.String())
		}
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// attachSecondWorktree gives a branch its own checkout inside the sandbox the parent
// already created — a second sandbox for one project is a conflict, which is the point
// of the (project, user) granularity.
func attachSecondWorktree(t *testing.T, env *testEnv, userID, projectID, parentID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	parent, err := env.store.Session(ctx, userID, parentID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	parentWT, err := env.store.Worktree(ctx, userID, parent.WorktreeID)
	if err != nil {
		t.Fatalf("load parent worktree: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: parentWT.SandboxID,
		Path:      parentWT.Path + "-" + sessionID,
		Branch:    "branch-" + sessionID,
	})
	if err != nil {
		t.Fatalf("create branch worktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sessionID, store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach branch worktree: %v", err)
	}
}
