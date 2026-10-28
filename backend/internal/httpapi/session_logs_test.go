package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// A crash that says nothing in the transcript usually said something on stderr.
// FR-5.10 is the difference between "the agent stopped" and "the agent stopped
// because it could not load the model".
func TestSessionLogsReturnTheAgentTail(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/logs", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Logs contracts.SessionLogs `json:"logs"`
		// Reason is set when the runtime keeps no logs at all.
		Reason string `json:"reason"`
	}
	env.mustJSON(rec, &body)
	if body.Reason != "" {
		t.Fatalf("the fake runtime should keep logs, got reason %q", body.Reason)
	}
	if len(body.Logs.Lines) == 0 {
		t.Fatal("no lines came back")
	}
	if body.Logs.SessionID != sess.ID {
		t.Fatalf("session id = %q", body.Logs.SessionID)
	}

	// Another user's session is not readable, and the response says 404 (not 403),
	// like every other cross-user access in this API.
	env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	other := env.login("other@piforge.local", "pw2")
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/logs", nil, other); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user logs = %d", rec.Code)
	}
}
