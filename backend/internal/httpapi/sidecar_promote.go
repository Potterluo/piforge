package httpapi

import (
	"context"
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

	// The branch starts as a *copy* of the side conversation, so the agent and the
	// transcript agree from the first message.
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
func (s *server) copyEvents(ctx context.Context, userID, fromID, toID string) error {
	events, err := s.deps.Store.ListEvents(ctx, userID, fromID, 0, 1000)
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
	return nil
}
