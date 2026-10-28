package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Support mode is the one place an admin can read somebody else's conversation,
// so the rules around it are the product promise: a reason is required, the grant
// expires, the owner is told, and nothing about it can mutate the session.
func TestSupportAccessIsRecordedReadOnlyAndVisibleToTheOwner(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	owner := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")
	_, sess := env.mkSessionFor(owner.ID, "repo")

	// The admin cannot see it through the ordinary endpoint: 404, not 403.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID, nil, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("an admin read a session without support mode: %d", rec.Code)
	}

	// A reason is mandatory.
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/support-access",
		map[string]any{"session_id": sess.ID}, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("support access without a reason = %d", rec.Code)
	}

	// With a reason, the transcript comes back and a receipt exists.
	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "r1", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgFinal, RunID: "r1", Payload: json.RawMessage(
			`{"role":"assistant","blocks":[{"kind":"text","index":0,"text":"the answer"}]}`)},
		{Type: contracts.EvRunSettled, RunID: "r1", Payload: json.RawMessage(`{}`)},
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, dev); rec.Code != http.StatusOK {
		t.Fatalf("seed conversation: %d", rec.Code)
	}
	for i := 0; i < 100; i++ {
		if events, _ := env.store.ListEvents(t.Context(), owner.ID, sess.ID, 0, 10); len(events) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/admin/support-access",
		map[string]any{"session_id": sess.ID, "reason": "the run is stuck at 99%"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("support access: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Access struct {
			ID      string `json:"id"`
			Reason  string `json:"reason"`
			OwnerID string `json:"owner_id"`
		} `json:"access"`
		Transcript []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(created.Transcript) == 0 || created.Transcript[0].Text != "the answer" {
		t.Fatalf("the transcript is missing the conversation: %s", rec.Body.String())
	}

	// The owner sees the receipt, with the reason.
	notices := env.doAuthed(http.MethodGet, "/v1/support-notices", nil, dev)
	if notices.Code != http.StatusOK {
		t.Fatalf("notices: %d", notices.Code)
	}
	var noticeBody struct {
		Notices []struct {
			ID           string `json:"id"`
			Reason       string `json:"reason"`
			AdminEmail   string `json:"admin_email"`
			SessionTitle string `json:"session_title"`
			SeenAt       any    `json:"seen_at"`
		} `json:"notices"`
		Unseen int `json:"unseen"`
	}
	if err := json.Unmarshal(notices.Body.Bytes(), &noticeBody); err != nil {
		t.Fatalf("decode notices: %v", err)
	}
	if len(noticeBody.Notices) != 1 || noticeBody.Unseen != 1 {
		t.Fatalf("owner notices = %+v", noticeBody)
	}
	if noticeBody.Notices[0].Reason != "the run is stuck at 99%" {
		t.Fatalf("the receipt lost the reason: %+v", noticeBody.Notices[0])
	}
	if noticeBody.Notices[0].AdminEmail != "admin@piforge.local" {
		t.Fatalf("the receipt does not name the admin: %+v", noticeBody.Notices[0])
	}
	if noticeBody.Notices[0].SeenAt != nil {
		t.Fatalf("a fresh receipt is already marked read: %+v", noticeBody.Notices[0])
	}

	// Marking it read clears the badge.
	if rec := env.doAuthed(http.MethodPost,
		"/v1/support-notices/"+noticeBody.Notices[0].ID+"/seen", nil, dev); rec.Code != http.StatusOK {
		t.Fatalf("mark seen: %d", rec.Code)
	}
	notices = env.doAuthed(http.MethodGet, "/v1/support-notices", nil, dev)
	if err := json.Unmarshal(notices.Body.Bytes(), &noticeBody); err != nil {
		t.Fatalf("decode notices: %v", err)
	}
	if noticeBody.Unseen != 0 {
		t.Fatalf("the receipt is still unseen: %+v", noticeBody)
	}

	// Read-only means read-only: the ordinary mutation path still refuses.
	if rec := env.doAuthed(http.MethodPatch, "/v1/sessions/"+sess.ID,
		map[string]any{"title": "renamed by support"}, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("support mode let an admin rename somebody's session: %d", rec.Code)
	}

	// And a normal user cannot open support access for anything.
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/support-access",
		map[string]any{"session_id": sess.ID, "reason": "because"}, dev); rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user opened support access: %d", rec.Code)
	}
}

// An expired grant must stop working, or "bounded window" is decoration.
func TestSupportGrantExpires(t *testing.T) {
	env := newTestEnv(t)
	adminUser := env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	owner := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	_, sess := env.mkSessionFor(owner.ID, "repo")

	if _, err := env.store.CreateSupportAccess(t.Context(), adminUser.ID, sess.ID, "debug"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Rewind the grant by hand: the store clock is fixed in tests.
	if err := env.store.ExecForTest(t.Context(),
		`UPDATE support_access SET expires_at = ? WHERE session_id = ?`,
		store.FormatTimeForTest(time.Now().Add(-time.Minute)), sess.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if rec := env.doAuthed(http.MethodGet,
		"/v1/admin/support-access/"+sess.ID+"/transcript", nil, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("an expired grant still read the transcript: %d", rec.Code)
	}
}
