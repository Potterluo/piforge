package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// seedHundreds appends n message.delta events named prefix-1..prefix-n. The
// payload matters: a merge test must be able to say *which* event it found, not
// only how many.
func seedHundreds(t *testing.T, env *testEnv, userID, sessionID, prefix string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		payload := json.RawMessage(fmt.Sprintf(`{"chunk":%q}`, fmt.Sprintf("%s-%d", prefix, i)))
		if _, err := env.store.AppendEvent(t.Context(), userID, &contracts.Event{
			SessionID: sessionID, Type: contracts.EvMsgDelta, Payload: payload,
		}); err != nil {
			t.Fatalf("AppendEvent(%s %d): %v", prefix, i, err)
		}
	}
}

// A branch used to open empty: Pi forked the agent's session file, so the model
// still had the conversation, but this control plane's event log started blank.
// The copy that fixed that later truncated at one ListEvents page; the lineage
// replaces the copy and must still carry the conversation.
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

	// The fake runtime replays its script into every process it starts, including
	// the branch's. The branch's real Pi process emits nothing before a prompt, so
	// clear it to keep this test about the inherited log, not the fake's replay.
	env.runtime.script = nil

	forked := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/fork",
		map[string]any{"entry_id": "e1", "label": "try another way"}, c)
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forked.Code, forked.Body.String())
	}
	var body struct {
		Session struct {
			ID          string `json:"id"`
			ForkedAtSeq int64  `json:"forked_at_seq"`
		} `json:"session"`
	}
	env.mustJSON(forked, &body)
	if body.Session.ForkedAtSeq != 3 {
		t.Fatalf("forked_at_seq = %d, want 3 (the parent's seq at fork time)", body.Session.ForkedAtSeq)
	}

	// The branch copied nothing; its transcript is the parent's log up to the cut.
	if own, err := env.store.ListEvents(t.Context(), user.ID, body.Session.ID, 0, 10); err != nil || len(own) != 0 {
		t.Fatalf("the fork copied events: %d, %v", len(own), err)
	}
	merged, err := env.store.ListMergedEvents(t.Context(), user.ID, body.Session.ID, 0, 50)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	found := false
	for _, event := range merged {
		if event.Type == contracts.EvMsgFinal {
			found = true
		}
	}
	if !found {
		t.Fatalf("the branch has no history: %d events", len(merged))
	}
}

// The defect: a 1500-event parent was copied with ListEvents(..., 0, 1000), so
// the branch's transcript stopped at event 1000 with no marker. The branch must
// now stream the whole log through the endpoint the UI actually reads.
func TestLongForkStreamsTheWholeTranscript(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("long@piforge.local", "pw", contracts.RoleUser)
	c := env.login("long@piforge.local", "pw")
	_, parent := env.mkSessionFor(user.ID, "repo")
	seedHundreds(t, env, user.ID, parent.ID, "event", 1500)

	forked := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/fork",
		map[string]any{"entry_id": "e1", "label": "long fork"}, c)
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forked.Code, forked.Body.String())
	}
	var body struct {
		Session struct {
			ID          string `json:"id"`
			ForkedAtSeq int64  `json:"forked_at_seq"`
		} `json:"session"`
	}
	env.mustJSON(forked, &body)
	if body.Session.ForkedAtSeq != 1500 {
		t.Fatalf("forked_at_seq = %d, want 1500", body.Session.ForkedAtSeq)
	}

	// No copy: forking a long session must not write 1500 events.
	if own, err := env.store.ListEvents(t.Context(), user.ID, body.Session.ID, 0, 10); err != nil || len(own) != 0 {
		t.Fatalf("the fork copied events: %d, %v", len(own), err)
	}

	// The UI reads /events. It used to replay 1000 frames and then go quiet;
	// now it must reach the parent's last event.
	srv := httptest.NewServer(env.handler)
	defer srv.Close()
	resp, cancel := openStream(t, srv, "/v1/sessions/"+body.Session.ID+"/events", c, "")
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	frame := readUntil(t, resp.Body, `"seq":1500`, 10*time.Second)
	if !containsFrameID(frame, 1) {
		t.Fatalf("the first event was not replayed:\n%.400s", frame)
	}
	if !containsChunk(frame, "event-1500") {
		t.Fatalf("the tail of the parent's transcript is missing:\n%.400s", frame)
	}
	if containsFrameID(frame, 1501) {
		t.Fatalf("the stream went past the parent's log:\n%.400s", frame)
	}
}

// forked_at_seq is a cut, not a live subscription: the parent may keep writing,
// and none of it may appear in the branch.
func TestForkMergeRespectsForkedAtSeq(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("cut@piforge.local", "pw", contracts.RoleUser)
	c := env.login("cut@piforge.local", "pw")
	_, parent := env.mkSessionFor(user.ID, "repo")
	seedHundreds(t, env, user.ID, parent.ID, "before", 40)

	forked := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/fork",
		map[string]any{"entry_id": "e1"}, c)
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forked.Code, forked.Body.String())
	}
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(forked, &body)

	// The parent is reused after the fork (another user turn, a recovery marker).
	seedHundreds(t, env, user.ID, parent.ID, "after", 20)

	merged, err := env.store.ListMergedEvents(t.Context(), user.ID, body.Session.ID, 0, 100)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	if len(merged) != 40 {
		t.Fatalf("merged transcript = %d events, want the frozen 40", len(merged))
	}
	for _, ev := range merged {
		var p struct {
			Chunk string `json:"chunk"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if len(p.Chunk) >= 6 && p.Chunk[:6] == "after-" {
			t.Fatalf("a post-fork parent event leaked into the branch: %q", p.Chunk)
		}
	}

	// A cursor at the end of the inherited block must resume, not 409: the
	// branch's own log is empty, so the transcript ends exactly at the cut.
	minSeq, maxSeq, err := env.store.MergedSeqRange(t.Context(), user.ID, body.Session.ID)
	if err != nil || minSeq != 1 || maxSeq != 40 {
		t.Fatalf("MergedSeqRange = %d..%d, %v; want 1..40", minSeq, maxSeq, err)
	}
}

// The branch's history lives in the parent's log. Deleting the parent would
// silently empty it, so the endpoint refuses and names the reason.
func TestDeletingAForkParentIsRefused(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("del@piforge.local", "pw", contracts.RoleUser)
	c := env.login("del@piforge.local", "pw")
	_, parent := env.mkSessionFor(user.ID, "repo")
	seedHundreds(t, env, user.ID, parent.ID, "question", 3)

	forked := env.doAuthed(http.MethodPost, "/v1/sessions/"+parent.ID+"/fork",
		map[string]any{"entry_id": "e1", "label": "keeps the history"}, c)
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forked.Code, forked.Body.String())
	}
	var body struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	env.mustJSON(forked, &body)

	rec := env.doAuthed(http.MethodDelete, "/v1/sessions/"+parent.ID, nil, c)
	env.mustErr(rec, http.StatusConflict, contracts.ErrConflict)

	// Both rows are still there and so is the inherited transcript.
	if rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+parent.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("parent after refused delete = %d", rec.Code)
	}
	merged, err := env.store.ListMergedEvents(t.Context(), user.ID, body.Session.ID, 0, 10)
	if err != nil || len(merged) != 3 {
		t.Fatalf("branch transcript after refused delete = %d, %v; want 3", len(merged), err)
	}

	// Deleting the branch first is the supported order; then the parent goes.
	if rec := env.doAuthed(http.MethodDelete, "/v1/sessions/"+body.Session.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("delete branch = %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.doAuthed(http.MethodDelete, "/v1/sessions/"+parent.ID, nil, c); rec.Code != http.StatusOK {
		t.Fatalf("delete parent after branch = %d %s", rec.Code, rec.Body.String())
	}
}

// containsFrameID reports whether the accumulated stream contains one SSE id.
func containsFrameID(frames string, id int64) bool {
	return strings.Contains(frames, fmt.Sprintf("id: %d\n", id))
}

func containsChunk(frames, chunk string) bool {
	return strings.Contains(frames, fmt.Sprintf(`"chunk":%q`, chunk))
}
