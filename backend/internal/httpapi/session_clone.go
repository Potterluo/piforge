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

// handleCloneSession copies a session into a new branch (PRD FR-2.7).
//
// The difference from a fork is the whole point: a fork truncates the conversation
// at a message and starts a *different* attempt from there, while a clone keeps
// everything and lets you try again from where you are. It is the "same conversation,
// second opinion" button, and it needs its own checkout so the two attempts cannot
// overwrite each other.
func (s *server) handleCloneSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	source, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if source.Kind == contracts.KindSidecar {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "旁路对话不能直接克隆，先把它提升为分支"))
		return
	}

	label := strings.TrimSpace(source.Label)
	if label == "" {
		label = source.Title
	}
	title := strings.TrimSpace(label) + " 的副本"

	rootID := source.RootID
	if rootID == "" {
		rootID = source.ID
	}
	clone, err := s.deps.Store.CreateSession(r.Context(), user.ID, store.NewSession{
		ProjectID: source.ProjectID,
		Kind:      contracts.KindBranch,
		RootID:    rootID,
		ParentID:  source.ID,
		Label:     title,
		Title:     title,
		ModelRef:  source.ModelRef,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	clone = s.provisionSession(r.Context(), w, user.ID, clone)

	// Both halves of the conversation: the events the page renders, and Pi's own
	// transcript that the model reads. Copying one without the other is the bug
	// that made a promoted branch deny its own history.
	if cloner, ok := s.deps.Runtime.(contracts.SessionCloner); ok {
		if err := cloner.CloneSession(context.WithoutCancel(r.Context()), source.ID, clone.ID); err != nil {
			s.log.WarnContext(r.Context(), "could not clone the agent transcript",
				"session", clone.ID, "error", err.Error())
		}
	}
	if err := s.copyEvents(context.WithoutCancel(r.Context()), user.ID, source.ID, clone.ID); err != nil {
		s.log.WarnContext(r.Context(), "could not copy the transcript events",
			"session", clone.ID, "error", err.Error())
	}

	startCtx, cancel := detach(r.Context(), 30*time.Second)
	startErr := s.hub.ensure(startCtx, user.ID, s.sessionSpec(user.ID, clone))
	cancel()
	if startErr != nil {
		s.log.WarnContext(r.Context(), "could not start the cloned session",
			"session", clone.ID, "error", startErr.Error())
	}

	writeJSON(w, http.StatusCreated, sessionResponse{Session: clone})
}
