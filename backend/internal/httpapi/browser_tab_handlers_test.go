package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// 新标签页 must be able to ask for a blank page by name. Before this, the UI
// opened a tab pointed at whatever URL was in the bar, and the URL bar rewrote
// about:blank to https://about:blank — so neither the browser nor the capture
// refusal could ever see a real blank page (QA-16, FIX D).
//
// The handler is exercised with no browser configured: the policy check runs
// before a launch, so "accepted" is a 422 from the missing Chrome and "refused"
// is a 400 that names the rule. That is the distinction the test is about.
func TestOpenBrowserTabAcceptsABlankPage(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "browser")
	env.withWorktree(user.ID, sess.ProjectID, sess.ID)

	for _, body := range []map[string]any{
		{"url": "about:blank"},
		// The UI's 新标签页 sends nothing at all; an empty url is the blank page.
		{"url": ""},
		{},
		{"url": "https://example.com"},
	} {
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/tabs", body, c)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("open tab %+v = %d %s, want 422 (the URL was accepted, no Chrome)", body, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "not an http(s) url") {
			t.Fatalf("open tab %+v was refused by the URL policy: %s", body, rec.Body.String())
		}
	}

	// A scheme the browser must not follow is still a 400 with the rule named.
	for _, refused := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/html,x"} {
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/tabs",
			map[string]any{"url": refused}, c)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("open tab %q = %d %s, want 400", refused, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "not an http(s) url") {
			t.Fatalf("open tab %q error does not name the rule: %s", refused, rec.Body.String())
		}
	}
}

// A deployment with the browser switched off must not panic on 新标签页: the
// handler dereferenced a nil manager and answered 500 through the recoverer.
func TestOpenBrowserTabWithoutABrowserIsUnavailableNotAPanic(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "browser")
	env.withWorktree(user.ID, sess.ProjectID, sess.ID)

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/tabs",
		map[string]any{"url": ""}, c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("open tab without a browser = %d %s", rec.Code, rec.Body.String())
	}
	env.mustErr(rec, http.StatusUnprocessableEntity, contracts.ErrSandboxUnavail)
}
