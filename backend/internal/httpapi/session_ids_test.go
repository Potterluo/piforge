package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// POST /v1/projects/{id}/sessions takes two ids from the body: `worktree_id` and
// `parent_id`. They are foreign keys, and a foreign key is still a name a tenant
// wrote: the handler used to store whatever it was given.
//
// The rows every read goes through are scoped by user (Store.Worktree and
// Store.Session both filter on user_id), so a forged id could not read another
// account's data — but it produced a session row that pointed at somebody else's
// fiducial, which is a lie the next reader has to discover. The check belongs at
// the boundary: an id that is not the caller's is a 404 before a row exists.
func TestCreateSessionRefusesAnIdThatIsNotTheCallers(t *testing.T) {
	env := newTestEnv(t)
	owner := env.mkUser("owner@piforge.local", "pw", contracts.RoleUser)
	other := env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	caller := env.login("owner@piforge.local", "pw")

	mineProj, mySess := env.mkSessionFor(owner.ID, "mine")
	_, theirs := env.mkSessionFor(other.ID, "theirs")

	create := func(body map[string]any) int {
		t.Helper()
		rec := env.doAuthed(http.MethodPost, "/v1/projects/"+mineProj.ID+"/sessions", body, caller)
		return rec.Code
	}

	// Another account's session as the parent: 404, and nothing was created.
	if code := create(map[string]any{"kind": "branch", "parent_id": theirs.ID}); code != http.StatusNotFound {
		t.Fatalf("forged parent_id = %d, want 404", code)
	}
	// A worktree that does not exist (or is somebody else's) is the same answer.
	if code := create(map[string]any{"kind": "main", "worktree_id": "wt_not_mine"}); code != http.StatusNotFound {
		t.Fatalf("forged worktree_id = %d, want 404", code)
	}

	// Nothing was inserted for either refusal.
	sessions, err := env.store.ListSessions(t.Context(), owner.ID, store.SessionFilter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("the refused creates left %d session(s), want only the fixture", len(sessions))
	}

	// Positive control: the caller's own session as the parent still works.
	if code := create(map[string]any{"kind": "branch", "parent_id": mySess.ID}); code != http.StatusCreated {
		t.Fatalf("own parent_id = %d, want 201", code)
	}
}
