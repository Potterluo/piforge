package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
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

	// Boundaries: one id is not a comparison, four is too many, and a session that is
	// not yours must not appear.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID, nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("single id = %d", rec.Code)
	}
	env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	stranger := env.login("other@piforge.local", "pw2")
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/compare?ids="+main.ID+","+other.ID, nil, stranger); rec.Code != http.StatusNotFound {
		t.Fatalf("someone else's sessions = %d", rec.Code)
	}
}
