package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// handlePromoteSidecar turns a side conversation into a real branch (PRD FR-4.7).
//
// A sidecar is deliberately cheap: it shares its parent's checkout, so it cannot
// change files independently. When the side conversation turns out to be the work
// you actually wanted, promoting it gives it its own worktree and its own place in
// the tree — and it keeps the conversation, which is the whole point ("带上它的
// 上下文").
func (s *server) handlePromoteSidecar(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sidecar, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if sidecar.Kind != contracts.KindSidecar {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "only a sidecar can be promoted"))
		return
	}
	if sidecar.ParentID == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "this sidecar has no parent session"))
		return
	}
	// Promoting mid-run copies whatever has been written so far and leaves the
	// sidecar running, so the branch would start empty while the UI says the
	// conversation came along. Wait for the turn instead of lying about it: a user
	// clicking just as the answer lands should get their branch, not an error.
	if err := s.waitUntilSettled(r.Context(), sidecar.ID, 20*time.Second); err != nil {
		s.fail(w, r, apiErr(contracts.ErrConflict, "这一轮还没结束，等它结束再提升"))
		return
	}
	parent, err := s.deps.Store.Session(r.Context(), user.ID, sidecar.ParentID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	title := strings.TrimSpace(sidecar.Title)
	if title == "" || title == "Sidecar" {
		title = "提升：" + parent.Title
	}

	rootID := parent.RootID
	if rootID == "" {
		rootID = parent.ID
	}
	branch, err := s.deps.Store.CreateSession(r.Context(), user.ID, store.NewSession{
		ProjectID:   sidecar.ProjectID,
		Kind:        contracts.KindBranch,
		RootID:      rootID,
		ParentID:    parent.ID,
		Label:       title,
		Title:       title,
		ModelRef:    sidecar.ModelRef,
		ForkEntryID: "",
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	branch = s.provisionSession(r.Context(), w, user.ID, branch)

	// Both sides have to end up with the side conversation: the events are what the
	// page displays, and Pi's own file is what the model reads. Copying only the
	// events produced a branch that showed the exchange while the agent answered
	// "this conversation starts fresh" — the same fiction a fork bug taught us to
	// avoid, one layer down.
	if cloner, ok := s.deps.Runtime.(contracts.SessionCloner); ok {
		if err := cloner.CloneSession(context.WithoutCancel(r.Context()), sidecar.ID, branch.ID); err != nil {
			s.log.WarnContext(r.Context(), "could not clone the sidecar's agent transcript",
				"session", branch.ID, "error", err.Error())
		}
	}
	if err := s.copyEvents(context.WithoutCancel(r.Context()), user.ID, sidecar.ID, branch.ID); err != nil {
		s.log.WarnContext(r.Context(), "could not copy the sidecar transcript",
			"session", branch.ID, "error", err.Error())
	}

	startCtx, cancel := detach(r.Context(), 30*time.Second)
	startErr := s.hub.ensure(startCtx, user.ID, s.sessionSpec(user.ID, branch))
	cancel()
	if startErr != nil {
		s.log.WarnContext(r.Context(), "could not start the promoted branch",
			"session", branch.ID, "error", startErr.Error())
	}

	// The sidecar stays as a record of the side conversation, archived so it does
	// not sit in the list as a duplicate of the branch.
	archived := contracts.StatusArchived
	if _, err := s.deps.Store.UpdateSession(context.WithoutCancel(r.Context()), user.ID, sidecar.ID,
		store.SessionPatch{Status: &archived}); err != nil {
		s.log.WarnContext(r.Context(), "could not archive the promoted sidecar",
			"session", sidecar.ID, "error", err.Error())
	}

	writeJSON(w, http.StatusCreated, sessionResponse{Session: branch})
}

// copyEvents duplicates one session's event log into another, renumbering the
// sequence so the target's own log stays contiguous.
//
// It pages without a cap for the same reason the fork no longer copies at all:
// one ListEvents call stops at 1000 events, and a clone or a promoted sidecar of
// a long conversation used to be silently cut there. Clone (FR-2.7) and promote
// (FR-4.7) both take this path, so both inherit the fix.
func (s *server) copyEvents(ctx context.Context, userID, fromID, toID string) error {
	const page = 500
	after := int64(0)
	for {
		events, err := s.deps.Store.ListEvents(ctx, userID, fromID, after, page)
		if err != nil {
			return err
		}
		for _, event := range events {
			copied := &contracts.Event{
				SessionID: toID,
				RunID:     event.RunID,
				EntryID:   event.EntryID,
				ParentID:  event.ParentID,
				Type:      event.Type,
				Payload:   event.Payload,
				TS:        event.TS,
			}
			if _, err := s.deps.Store.AppendEvent(ctx, userID, copied); err != nil {
				return err
			}
		}
		if len(events) < page {
			return nil
		}
		after = events[len(events)-1].Seq
	}
}

// waitUntilSettled polls the runtime until no turn is in flight, or the budget runs
// out. Polling is enough: the runtime reports a boolean, and a subscription would be
// a lot of machinery for a 20-second wait.
func (s *server) waitUntilSettled(ctx context.Context, sessionID string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		status, err := s.deps.Runtime.Status(ctx, sessionID)
		if err != nil || !status.Running {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("still running")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
