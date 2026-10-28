package httpapi

import (
	"context"
	"fmt"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// seedForkedHistory gives a new branch the transcript of the session it came from.
//
// Pi forks the *agent* session file, so the model keeps its context — but the
// workspace renders from this control plane's event log, which starts empty for a
// new session. The result was a branch that could answer questions about a
// conversation it appeared never to have had. Copying the parent's events up to
// the fork point is what makes the two agree.
//
// The lineage comes from the events themselves (entry_id → parent_id), so this
// works whether or not the parent's agent is running.
func (s *server) seedForkedHistory(ctx context.Context, userID, parentID, childID, forkEntryID string) error {
	parentEvents, err := s.deps.Store.ListEvents(ctx, userID, parentID, 0, 1000)
	if err != nil || len(parentEvents) == 0 {
		return err
	}

	// Walk the tree backwards from the fork point, using the parent links the
	// events already carry.
	parentOf := make(map[string]string, len(parentEvents))
	for _, event := range parentEvents {
		if event.EntryID != "" {
			parentOf[event.EntryID] = event.ParentID
		}
	}
	keep := make(map[string]bool, len(parentOf))
	for id := forkEntryID; id != ""; {
		if keep[id] {
			break // a malformed chain must not loop forever
		}
		keep[id] = true
		next, ok := parentOf[id]
		if !ok {
			break
		}
		id = next
	}

	lastKept := int64(0)
	selected := make([]*contracts.Event, 0, len(parentEvents))
	for _, event := range parentEvents {
		if event.EntryID != "" && keep[event.EntryID] {
			selected = append(selected, event)
			lastKept = event.Seq
		}
	}
	if len(selected) == 0 {
		// The fork point is not in the log (the parent may have been forked before
		// its history was recorded). Copying everything beats copying nothing: a
		// branch with more history than it should show is less confusing than one
		// that repeats "I never saw that".
		selected = parentEvents
		lastKept = parentEvents[len(parentEvents)-1].Seq
	} else {
		// Run-level events (started, settled) carry no entry id but belong to the
		// window; without them the copied transcript has no turn boundaries.
		withRuns := make([]*contracts.Event, 0, len(selected)+8)
		for _, event := range parentEvents {
			if event.EntryID == "" && event.Seq <= lastKept {
				withRuns = append(withRuns, event)
				continue
			}
			if event.EntryID != "" && keep[event.EntryID] {
				withRuns = append(withRuns, event)
			}
		}
		selected = withRuns
	}

	for _, event := range selected {
		copied := &contracts.Event{
			SessionID: childID,
			RunID:     event.RunID,
			EntryID:   event.EntryID,
			ParentID:  event.ParentID,
			Type:      event.Type,
			Payload:   event.Payload,
			TS:        event.TS,
		}
		if _, err := s.deps.Store.AppendEvent(ctx, userID, copied); err != nil {
			return fmt.Errorf("seed fork history: %w", err)
		}
	}
	return nil
}

// markForkFailed is the shared cleanup for a failed fork: the row stays, marked,
// so the user can see what happened instead of finding a silent hole.
func (s *server) markForkFailed(ctx context.Context, userID, sessionID string) {
	failed := contracts.StatusFailed
	if _, err := s.deps.Store.UpdateSession(ctx, userID, sessionID, store.SessionPatch{Status: &failed}); err != nil {
		s.log.WarnContext(ctx, "could not mark forked session failed", "session", sessionID, "error", err.Error())
	}
}
