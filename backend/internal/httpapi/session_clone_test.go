package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-2.7: a clone keeps the whole conversation and gives you a second attempt with
// its own checkout. Unlike a fork it truncates nothing, which is the difference the
// two buttons exist to express.
func TestCloneKeepsTheWholeConversation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, source := env.mkSessionFor(user.ID, "repo")

	// A scripted run, so the source session has a conversation to copy.
	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "r1", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgFinal, RunID: "r1", Payload: json.RawMessage(
			`{"role":"assistant","blocks":[{"kind":"text","index":0,"text":"an answer"}]}`)},
		{Type: contracts.EvRunSettled, RunID: "r1", Payload: json.RawMessage(`{}`)},
	}

	// Two turns in the source session.
	for _, text := range []string{"first question", "second question"} {
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+source.ID+"/messages",
			map[string]any{"client_msg_id": "cm_" + text, "text": text}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("prompt: %d %s", rec.Code, rec.Body.String())
		}
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+source.ID+"/clone", nil, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body.String())
	}
	var cloned struct {
		Session struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			ParentID string `json:"parent_id"`
			Title    string `json:"title"`
		} `json:"session"`
	}
	env.mustJSON(rec, &cloned)
	if cloned.Session.Kind != string(contracts.KindBranch) {
		t.Fatalf("clone kind = %q", cloned.Session.Kind)
	}
	if cloned.Session.ParentID != source.ID {
		t.Fatalf("clone parent = %q, want %q", cloned.Session.ParentID, source.ID)
	}
	if cloned.Session.ID == source.ID {
		t.Fatal("clone reused the source session")
	}

	// The conversation came along — both halves. The hub pumps a run's events
	// asynchronously, so give it a moment to land.
	var events []*contracts.Event
	var err error
	for i := 0; i < 100; i++ {
		events, err = env.store.ListEvents(t.Context(), user.ID, cloned.Session.ID, 0, 100)
		if err == nil && len(events) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("clone events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the clone has no events")
	}
	env.runtime.mu.Lock()
	clonedFrom := append([]string(nil), env.runtime.cloned...)
	env.runtime.mu.Unlock()
	if len(clonedFrom) == 0 || clonedFrom[0] != source.ID+"->"+cloned.Session.ID {
		t.Fatalf("agent transcript not cloned: %v", clonedFrom)
	}

	// A sidecar cannot be cloned: it has no checkout of its own to copy.
	side := env.doAuthed(http.MethodPost, "/v1/sessions/"+source.ID+"/sidecar", nil, c)
	var created struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(side, &created)
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+created.Session.ID+"/clone", nil, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("cloning a sidecar = %d", rec.Code)
	}
}

// The copy helper behind clone (and promote) used to read one ListEvents page,
// which stops at 1000 events. A clone of a longer conversation was silently cut
// there. It must copy every page now.
func TestCloneCopiesPastTheOldPageCap(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("longclone@piforge.local", "pw", contracts.RoleUser)
	c := env.login("longclone@piforge.local", "pw")
	_, source := env.mkSessionFor(user.ID, "repo")
	seedHundreds(t, env, user.ID, source.ID, "copy", 1200)
	// The clone's fake agent must not replay the process-wide script into it;
	// this test is about copyEvents, not the runtime.
	env.runtime.script = nil

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+source.ID+"/clone", nil, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body.String())
	}
	var cloned struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(rec, &cloned)

	after := int64(0)
	total := 0
	for {
		batch, err := env.store.ListEvents(t.Context(), user.ID, cloned.Session.ID, after, 500)
		if err != nil {
			t.Fatalf("clone events: %v", err)
		}
		total += len(batch)
		if len(batch) == 0 {
			break
		}
		after = batch[len(batch)-1].Seq
		if len(batch) < 500 {
			break
		}
	}
	if total != 1200 {
		t.Fatalf("clone copied %d events, want 1200", total)
	}
}
