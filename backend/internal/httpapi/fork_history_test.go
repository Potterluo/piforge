package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// A branch used to open empty: Pi forked the agent's session file, so the model
// still had the conversation, but this control plane's event log started blank and
// the transcript showed nothing. Asking the model about it then produced "I never
// saw that", which is the worst kind of wrong.
func TestForkedBranchCarriesTheConversation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	// Two turns in the parent, each with an entry id so the lineage is walkable.
	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "r1", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgFinal, RunID: "r1", EntryID: "e1", Payload: json.RawMessage(
			`{"role":"assistant","blocks":[{"kind":"text","index":0,"text":"first answer"}]}`)},
		{Type: contracts.EvRunSettled, RunID: "r1", EntryID: "e2", Payload: json.RawMessage(`{}`)},
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "first question"}, c); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 100; i++ {
		if events, _ := env.store.ListEvents(t.Context(), user.ID, sess.ID, 0, 10); len(events) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	forked := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/fork",
		map[string]any{"entry_id": "e1", "label": "try another way"}, c)
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forked.Code, forked.Body.String())
	}
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(forked, &body)

	events, err := env.store.ListEvents(t.Context(), user.ID, body.Session.ID, 0, 50)
	if err != nil {
		t.Fatalf("child events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == contracts.EvMsgFinal {
			found = true
		}
	}
	if !found {
		t.Fatalf("the branch has no history: %d events", len(events))
	}
}
