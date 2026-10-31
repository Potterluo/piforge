package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// seedLog appends n events with a payload that names its position, so a merge
// test can say which event it is looking at instead of only counting.
func seedLog(t *testing.T, s *Store, userID, sessionID, prefix string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		payload := json.RawMessage(fmt.Sprintf(`{"chunk":%q}`, fmt.Sprintf("%s-%d", prefix, i)))
		if _, err := s.AppendEvent(context.Background(), userID, &contracts.Event{
			SessionID: sessionID,
			Type:      contracts.EvMsgDelta,
			Payload:   payload,
		}); err != nil {
			t.Fatalf("AppendEvent(%s %d): %v", prefix, i, err)
		}
	}
}

func chunkOf(t *testing.T, ev *contracts.Event) string {
	t.Helper()
	var body struct {
		Chunk string `json:"chunk"`
	}
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		t.Fatalf("payload %s: %v", ev.Payload, err)
	}
	return body.Chunk
}

func allMerged(t *testing.T, s *Store, userID, sessionID string, page int) []*contracts.Event {
	t.Helper()
	var all []*contracts.Event
	after := int64(0)
	for {
		batch, err := s.ListMergedEvents(context.Background(), userID, sessionID, after, page)
		if err != nil {
			t.Fatalf("ListMergedEvents(after=%d): %v", after, err)
		}
		all = append(all, batch...)
		if len(batch) < page {
			return all
		}
		after = batch[len(batch)-1].Seq
		if len(all) > 100000 {
			t.Fatal("ListMergedEvents does not terminate")
		}
	}
}

// A fork of a 1500-event parent used to copy only the first 1000 events (one
// ListEvents page). The branch transcript must now contain all 1500, in order.
func TestMergedTranscriptKeepsA1500EventParent(t *testing.T) {
	s := newTestStore(t)
	u := mkUser(t, s, "fork@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	parent := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	seedLog(t, s, u.ID, parent.ID, "parent", 1500)

	// The cut the fork handler records is the parent transcript's last seq.
	child, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: parent.ID,
		ParentID: parent.ID, ForkedAtSeq: 1500, Title: "fork",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if child.ForkedAtSeq != 1500 {
		t.Fatalf("ForkedAtSeq = %d, want 1500", child.ForkedAtSeq)
	}

	// Nothing was copied: the branch's own log is empty.
	if own, err := s.ListEvents(context.Background(), u.ID, child.ID, 0, 10); err != nil || len(own) != 0 {
		t.Fatalf("the fork copied events: %d, %v", len(own), err)
	}

	all := allMerged(t, s, u.ID, child.ID, 200)
	if len(all) != 1500 {
		t.Fatalf("merged transcript = %d events, want 1500", len(all))
	}
	if all[0].Seq != 1 || chunkOf(t, all[0]) != "parent-1" {
		t.Fatalf("first event = seq %d %q, want 1 parent-1", all[0].Seq, chunkOf(t, all[0]))
	}
	last := all[len(all)-1]
	if last.Seq != 1500 || chunkOf(t, last) != "parent-1500" {
		t.Fatalf("last event = seq %d %q, want 1500 parent-1500", last.Seq, chunkOf(t, last))
	}
}

// The cut is a fact, not a moving window: events the parent writes after the
// fork are outside the branch's transcript, and the branch's own events follow
// the inherited block without a collision.
func TestMergedTranscriptStopsAtTheForkSeq(t *testing.T) {
	s := newTestStore(t)
	u := mkUser(t, s, "cut@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	parent := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	seedLog(t, s, u.ID, parent.ID, "before", 1200)

	child, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: parent.ID,
		ParentID: parent.ID, ForkedAtSeq: 1200, Title: "fork",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The parent keeps talking after the branch was made.
	seedLog(t, s, u.ID, parent.ID, "after", 300)
	seedLog(t, s, u.ID, child.ID, "branch", 2)

	all := allMerged(t, s, u.ID, child.ID, 200)
	if len(all) != 1202 {
		t.Fatalf("merged transcript = %d events, want 1202", len(all))
	}
	for _, ev := range all {
		if ev.SessionID == parent.ID && ev.Seq > 1200 {
			t.Fatalf("a post-fork parent event leaked in: %s seq %d", chunkOf(t, ev), ev.Seq)
		}
		if ev.SessionID == parent.ID && ev.Seq <= 1200 && chunkOf(t, ev) != fmt.Sprintf("before-%d", ev.Seq) {
			t.Fatalf("parent event %d = %q, want before-%d", ev.Seq, chunkOf(t, ev), ev.Seq)
		}
	}
	if got := chunkOf(t, all[1200]); got != "branch-1" {
		t.Fatalf("first own event after the cut = %q, want branch-1 (seq %d)", got, all[1200].Seq)
	}
	if all[1200].Seq != 1201 || all[1201].Seq != 1202 {
		t.Fatalf("own events continue at seqs %d, %d; want 1201, 1202", all[1200].Seq, all[1201].Seq)
	}

	minSeq, maxSeq, err := s.MergedSeqRange(context.Background(), u.ID, child.ID)
	if err != nil {
		t.Fatalf("MergedSeqRange: %v", err)
	}
	if minSeq != 1 || maxSeq != 1202 {
		t.Fatalf("MergedSeqRange = %d..%d, want 1..1202", minSeq, maxSeq)
	}
}

// A fork of a fork cuts inside the parent's *merged* transcript: the child
// inherits the grandparent's prefix and the parent's own events up to the cut,
// and nothing after it.
func TestMergedTranscriptHonoursNestedCuts(t *testing.T) {
	s := newTestStore(t)
	u := mkUser(t, s, "nested@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	root := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	seedLog(t, s, u.ID, root.ID, "root", 100)

	first, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: root.ID,
		ParentID: root.ID, ForkedAtSeq: 80, Title: "first",
	})
	if err != nil {
		t.Fatalf("CreateSession(first): %v", err)
	}
	seedLog(t, s, u.ID, first.ID, "first", 10)

	// first's merged transcript is root 1..80 then first 81..90.
	second, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: root.ID,
		ParentID: first.ID, ForkedAtSeq: 90, Title: "second",
	})
	if err != nil {
		t.Fatalf("CreateSession(second): %v", err)
	}
	seedLog(t, s, u.ID, second.ID, "second", 3)

	all := allMerged(t, s, u.ID, second.ID, 7)
	if len(all) != 93 {
		t.Fatalf("nested merged transcript = %d events, want 93", len(all))
	}
	if got := chunkOf(t, all[79]); got != "root-80" {
		t.Fatalf("event 80 = %q, want root-80", got)
	}
	if got := chunkOf(t, all[80]); got != "first-1" {
		t.Fatalf("event 81 = %q, want first-1", got)
	}
	if got := chunkOf(t, all[89]); got != "first-10" {
		t.Fatalf("event 90 = %q, want first-10", got)
	}
	if got := chunkOf(t, all[90]); got != "second-1" {
		t.Fatalf("event 91 = %q, want second-1", got)
	}

	// A fork cut inside the grandparent block drops the parent's own events:
	// the branch was made before the parent said anything.
	early, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: root.ID,
		ParentID: first.ID, ForkedAtSeq: 40, Title: "early",
	})
	if err != nil {
		t.Fatalf("CreateSession(early): %v", err)
	}
	seedLog(t, s, u.ID, early.ID, "early", 1)
	earlyEvents := allMerged(t, s, u.ID, early.ID, 9)
	if len(earlyEvents) != 41 {
		t.Fatalf("early fork = %d events, want 41", len(earlyEvents))
	}
	if got := chunkOf(t, earlyEvents[40]); got != "early-1" || earlyEvents[40].Seq != 41 {
		t.Fatalf("early own event = seq %d %q, want 41 early-1", earlyEvents[40].Seq, got)
	}
}

// The delete endpoint refuses to remove a session that forks inherit from. If a
// row disappears by another route anyway (a project cascade, a manual delete),
// the read must not error and must not invent the missing parent's events.
func TestMergedTranscriptSurvivesADeletedParent(t *testing.T) {
	s := newTestStore(t)
	u := mkUser(t, s, "orphan@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	parent := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	seedLog(t, s, u.ID, parent.ID, "parent", 10)

	child, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: parent.ID,
		ParentID: parent.ID, ForkedAtSeq: 10, Title: "fork",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedLog(t, s, u.ID, child.ID, "branch", 1)

	forks, err := s.CountForkChildren(context.Background(), u.ID, parent.ID)
	if err != nil || forks != 1 {
		t.Fatalf("CountForkChildren = %d, %v; want 1", forks, err)
	}

	// Bypass the API's refusal, as a project cascade would.
	if err := s.DeleteSession(context.Background(), u.ID, parent.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	all := allMerged(t, s, u.ID, child.ID, 10)
	if len(all) != 1 || chunkOf(t, all[0]) != "branch-1" {
		t.Fatalf("merged after parent delete = %d events %+v, want just the branch's own", len(all), all)
	}
	_, _, err = s.MergedSeqRange(context.Background(), u.ID, child.ID)
	if err != nil {
		t.Fatalf("MergedSeqRange after parent delete: %v", err)
	}
}

// A fresh fork has written no context.usage event of its own; its meter must
// show the context it inherited instead of dropping to zero.
func TestMergedTranscriptCarriesTheInheritedContextUsage(t *testing.T) {
	s := newTestStore(t)
	u := mkUser(t, s, "usage@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "repo")
	parent := mkSession(t, s, u.ID, p.ID, contracts.KindMain)
	seedLog(t, s, u.ID, parent.ID, "parent", 5)
	if _, err := s.AppendEvent(context.Background(), u.ID, &contracts.Event{
		SessionID: parent.ID, Type: contracts.EvContextUsage,
		Payload: json.RawMessage(`{"context_tokens":48000,"context_window":128000}`),
	}); err != nil {
		t.Fatalf("AppendEvent(context.usage): %v", err)
	}

	child, err := s.CreateSession(context.Background(), u.ID, NewSession{
		ProjectID: p.ID, Kind: contracts.KindBranch, RootID: parent.ID,
		ParentID: parent.ID, ForkedAtSeq: 6, Title: "fork",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	tokens, window, ok, err := s.LastContextUsage(context.Background(), u.ID, child.ID)
	if err != nil || !ok || tokens != 48000 || window != 128000 {
		t.Fatalf("inherited context = %d/%d ok=%v err=%v, want 48000/128000", tokens, window, ok, err)
	}
}
