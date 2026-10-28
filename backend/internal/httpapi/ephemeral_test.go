package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-1.10: scratch sessions exist so a quick question does not become an artifact.
// They stay out of the list, and they expire.
func TestEphemeralSessionsAreHiddenAndSwept(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, kept := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+project.ID+"/sessions",
		map[string]any{"title": "quick look", "ephemeral": true}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ephemeral: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session struct {
			ID        string `json:"id"`
			Ephemeral bool   `json:"ephemeral"`
		} `json:"session"`
	}
	env.mustJSON(rec, &created)
	if !created.Session.Ephemeral {
		t.Fatal("the response does not say the session is ephemeral")
	}

	// Hidden from the project's list, which is what the sidebar shows.
	list := env.doAuthed(http.MethodGet, "/v1/projects/"+project.ID+"/sessions", nil, c)
	var listed struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	env.mustJSON(list, &listed)
	for _, item := range listed.Sessions {
		if item.ID == created.Session.ID {
			t.Fatal("an ephemeral session appeared in the project list")
		}
	}

	// Still readable: opening it by id has to work, or it would be unreachable.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+created.Session.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("read ephemeral by id = %d", rec.Code)
	}

	// The sweeper removes it once it has been idle past the cutoff.
	if n, err := env.store.SweepEphemeral(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if n != 1 {
		t.Fatalf("swept %d sessions, want 1", n)
	}
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+created.Session.ID, nil, c); rec.Code != http.StatusNotFound {
		t.Fatalf("swept session still readable: %d", rec.Code)
	}
	// A normal session is untouched.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+kept.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("the sweeper removed a normal session: %d", rec.Code)
	}
}
