package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Promoting a sidecar (PRD FR-4.7) has to produce a branch that owns what the side
// conversation said and can change files on its own. Anything less is a copy with
// extra steps.
func TestPromptedSidecarBecomesABranchWithItsConversation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, parent := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, parent.ID)

	// A sidecar with one exchange in it.
	sidecar := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/sidecar", nil, c)
	if sidecar.Code != http.StatusCreated {
		t.Fatalf("sidecar: %d %s", sidecar.Code, sidecar.Body.String())
	}
	var created struct {
		Session struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"session"`
	}
	env.mustJSON(sidecar, &created)
	if created.Session.Kind != string(contracts.KindSidecar) {
		t.Fatalf("kind = %q", created.Session.Kind)
	}

	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "r1", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgFinal, RunID: "r1", Payload: json.RawMessage(
			`{"role":"assistant","blocks":[{"kind":"text","index":0,"text":"side note"}]}`)},
		{Type: contracts.EvRunSettled, RunID: "r1", Payload: json.RawMessage(`{}`)},
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+created.Session.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "a thought on the side"}, c); rec.Code != http.StatusOK {
		t.Fatalf("sidecar prompt: %d %s", rec.Code, rec.Body.String())
	}
	// Promote refuses while a turn is in flight, so wait for the run to settle.
	for i := 0; i < 200; i++ {
		status, err := env.runtime.Status(t.Context(), created.Session.ID)
		events, _ := env.store.ListEvents(t.Context(), user.ID, created.Session.ID, 0, 10)
		if err == nil && !status.Running && len(events) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	promoted := env.doAuthed(http.MethodPost, "/v1/sessions/"+created.Session.ID+"/promote", nil, c)
	if promoted.Code != http.StatusCreated {
		t.Fatalf("promote: %d %s", promoted.Code, promoted.Body.String())
	}
	var branch struct {
		Session struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"session"`
	}
	env.mustJSON(promoted, &branch)
	if branch.Session.Kind != string(contracts.KindBranch) {
		t.Fatalf("promoted kind = %q", branch.Session.Kind)
	}
	if branch.Session.ID == created.Session.ID {
		t.Fatal("promote reused the sidecar instead of creating a branch")
	}

	// The conversation came along.
	events, err := env.store.ListEvents(t.Context(), user.ID, branch.Session.ID, 0, 50)
	if err != nil {
		t.Fatalf("branch events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == contracts.EvMsgFinal {
			found = true
		}
	}
	if !found {
		t.Fatalf("the promoted branch has no transcript: %d events", len(events))
	}

	// The agent side was seeded too, not only the display side.
	env.runtime.mu.Lock()
	cloned := append([]string(nil), env.runtime.cloned...)
	env.runtime.mu.Unlock()
	if len(cloned) == 0 {
		t.Fatal("the promoted branch never got the sidecar's agent transcript")
	}
	if cloned[0] != created.Session.ID+"->"+branch.Session.ID {
		t.Fatalf("cloned %q, want sidecar->branch", cloned[0])
	}

	// The sidecar is archived, not deleted: the record of the side conversation
	// stays available.
	after, err := env.store.Session(t.Context(), user.ID, created.Session.ID)
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if after.Status != contracts.StatusArchived {
		t.Fatalf("sidecar status = %q, want archived", after.Status)
	}

	// Promoting something that is not a sidecar is a client error.
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/promote", nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("promoting a main session = %d", rec.Code)
	}
}
