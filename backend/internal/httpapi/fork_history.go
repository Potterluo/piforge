package httpapi

import (
	"context"
	"fmt"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// recordForkLineage makes the branch inherit its parent's transcript instead of
// copying a page of it.
//
// The old seedForkedHistory paged the parent's event log with
// ListEvents(..., 0, 1000). ListEvents is the SSE replay query and caps a page
// at 1000, so a long parent was copied only up to event 1000: the branch showed
// a conversation that stopped in the middle, with no marker and no warning. The
// entry-id lineage that was supposed to select a window was dead in this build
// too — Pi does not report entry ids on its event stream, so every stored
// event's EntryID is empty and the walk fell back to "copy everything [it could
// read]".
//
// A cut is better than a copy:
//   - the parent log is the single source of truth, so the branch cannot be
//     truncated by a page size or drift from the parent;
//   - the cut is one number, frozen at fork time, so the parent's later events
//     are outside the branch's window and cannot leak in;
//   - no write amplification: forking a 50k-event session stays O(1).
//
// cutoff is the parent's transcript seq at the moment of the fork (see
// Store.MergedSeqRange). It is read before the fork so the branch inherits
// exactly what the user could see when they pressed the button.
func (s *server) forkInheritedSeq(ctx context.Context, userID, parentID string) int64 {
	_, maxSeq, err := s.deps.Store.MergedSeqRange(ctx, userID, parentID)
	if err != nil {
		s.log.WarnContext(ctx, "could not measure the parent transcript",
			"session", parentID, "error", err.Error())
		return 0
	}
	return maxSeq
}

// recordForkLineage writes the cut onto the branch. It runs only once the
// branch has proved it carries the agent's history: forking at the first user
// message produces an empty Pi session, and showing the parent's transcript to
// a model that never saw it is the fiction the old code was careful to avoid.
func (s *server) recordForkLineage(ctx context.Context, userID, childID string, cutoff int64) error {
	if cutoff <= 0 {
		return nil
	}
	if _, err := s.deps.Store.UpdateSession(ctx, userID, childID,
		store.SessionPatch{ForkedAtSeq: &cutoff}); err != nil {
		return fmt.Errorf("record fork lineage: %w", err)
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
