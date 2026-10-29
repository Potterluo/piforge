package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The status screen is admin-only and counts real rows: an operator opens it when
// something already feels wrong, so it has to be right rather than pretty.
func TestAdminStatusCountsAndIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	adminUser := env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")
	env.mkSessionFor(adminUser.ID, "repo")
	// FR-3.3: the effective parallel cap is part of the operator's screen, so
	// "what is it set to" does not require reading the startup flag.
	env.deps.ParallelLimit = 5
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Users            int `json:"users"`
		UsersActive      int `json:"users_active"`
		Sessions         int `json:"sessions"`
		Projects         int `json:"projects"`
		Providers        int `json:"providers"`
		ParallelLimit    int `json:"parallel_limit"`
		ParallelLimitMax int `json:"parallel_limit_max"`
	}
	env.mustJSON(rec, &body)
	if body.Users < 2 || body.Sessions < 1 || body.Projects < 1 {
		t.Fatalf("counts look empty: %+v", body)
	}
	if body.UsersActive != body.Users {
		t.Fatalf("every user in this fixture is active: %+v", body)
	}
	if body.ParallelLimit != 5 {
		t.Fatalf("parallel_limit = %d, want the configured 5", body.ParallelLimit)
	}
	if body.ParallelLimitMax != MaxParallelLimit {
		t.Fatalf("parallel_limit_max = %d, want %d", body.ParallelLimitMax, MaxParallelLimit)
	}

	if rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, dev); rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user read the status page: %d", rec.Code)
	}
}

// The normalizer fills both payload.Text and the text blocks, so an export that
// used both printed every message twice. This asserts one occurrence per message.
func TestExportDoesNotDuplicateMessages(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "r1", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgFinal, RunID: "r1", Payload: json.RawMessage(
			`{"role":"assistant","text":"only once","blocks":[{"kind":"text","index":0,"text":"only once"}]}`)},
		{Type: contracts.EvRunSettled, RunID: "r1", Payload: json.RawMessage(`{}`)},
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c); rec.Code != http.StatusOK {
		t.Fatalf("send: %d", rec.Code)
	}
	for i := 0; i < 100; i++ {
		if events, _ := env.store.ListEvents(t.Context(), user.ID, sess.ID, 0, 10); len(events) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/export?format=md", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), "only once"); got != 1 {
		t.Fatalf("the message appears %d times: %s", got, rec.Body.String())
	}
	// RFC 5987 so a Chinese title survives, with an ASCII fallback for old clients.
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "filename*=UTF-8''") || !strings.Contains(disposition, "filename=") {
		t.Fatalf("Content-Disposition is not RFC 5987: %q", disposition)
	}
}
