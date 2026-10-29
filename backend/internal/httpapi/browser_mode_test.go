package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-11.2 / FR-11.4: watching is the default, and taking over is explicit. The switch is
// enforced on the server because a client that forgets to check must not be able to type
// into a page the user is only watching.
func TestBrowserModeDefaultsToObserve(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "browser")
	env.withWorktree(user.ID, sess.ProjectID, sess.ID)

	var mode struct {
		Mode string `json:"mode"`
	}
	env.mustJSON(env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/browser/mode", nil, c), &mode)
	if mode.Mode != "observe" {
		t.Fatalf("default mode = %q", mode.Mode)
	}

	// Taking over is recorded and read back.
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("takeover: %d %s", rec.Code, rec.Body.String())
	}
	env.mustJSON(env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/browser/mode", nil, c), &mode)
	if mode.Mode != "takeover" {
		t.Fatalf("mode after takeover = %q", mode.Mode)
	}

	// Going back to observe clears the entry, so a restart of the page is not needed.
	env.mustJSON(env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/mode",
		map[string]any{"mode": "observe"}, c), &mode)
	if mode.Mode != "observe" {
		t.Fatalf("mode after observe = %q", mode.Mode)
	}

	// An unknown mode is refused rather than silently treated as observe.
	bad := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/mode",
		map[string]any{"mode": "drive"}, c)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d", bad.Code)
	}
	if !strings.Contains(bad.Body.String(), "observe or takeover") {
		t.Fatalf("message = %s", bad.Body.String())
	}
}

// Another user's session is not a place to flip a control switch.
func TestBrowserModeIsPerSessionAndOwned(t *testing.T) {
	env := newTestEnv(t)
	owner := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	other := env.mkUser("other@piforge.local", "pw", contracts.RoleUser)
	ownerCreds := env.login("dev@piforge.local", "pw")
	otherCreds := env.login("other@piforge.local", "pw")
	_, sess := env.mkSessionFor(owner.ID, "browser")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, otherCreds)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another user could set the mode: %d", rec.Code)
	}
	_ = other
	if browserModeOf(sess.ID) != browserModeObserve {
		t.Fatal("the mode was changed by a user who does not own the session")
	}
	_ = ownerCreds
}
