package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// messageRequest is POST /v1/sessions/{id}/messages.
type messageRequest struct {
	// ClientMsgID makes the submission idempotent per session (PRD §7.4.5).
	ClientMsgID string            `json:"client_msg_id"`
	Text        string            `json:"text"`
	Images      []contracts.Image `json:"images"`
	// StreamingBehavior is "", "steer" or "follow_up"; it is only meaningful
	// while a run is in flight.
	StreamingBehavior string `json:"streaming_behavior"`
	// ModelRef switches the model for this session before prompting
	// (PRD FR-13.6) and persists, so a reconnect keeps the choice.
	ModelRef string `json:"model_ref"`
}

// handlePostMessage submits a user message.
//
// Order matters and follows PRD §7.3: the message is made idempotent and
// persisted only after the runtime accepted it, but the (session,
// client_msg_id) pair is checked first so a retry never double-prompts.
func (s *server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var in messageRequest
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	// Accept either the body field or the Idempotency-Key header (PRD §9.1).
	clientID := strings.TrimSpace(in.ClientMsgID)
	if clientID == "" {
		clientID = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if clientID == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "client_msg_id is required"))
		return
	}
	if len(clientID) > 200 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "client_msg_id is too long"))
		return
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Images) == 0 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "text or images are required"))
		return
	}
	switch in.StreamingBehavior {
	case "", "steer", "follow_up":
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"streaming_behavior must be empty, steer or follow_up"))
		return
	}

	// Replay: return exactly what the first submission produced.
	if rec, err := s.deps.Store.Message(r.Context(), user.ID, sess.ID, clientID); err == nil {
		writeJSON(w, http.StatusOK, promptResultFromRecord(rec))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}

	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A per-session model override sticks to the session, not the request.
	if in.ModelRef != "" && in.ModelRef != sess.ModelRef {
		updated, err := s.deps.Store.UpdateSession(r.Context(), user.ID, sess.ID,
			store.SessionPatch{ModelRef: &in.ModelRef})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sess = updated
	}

	// The first prompt for a session should run inside its checkout. Best
	// effort: a provisioning failure leaves spec.Worktree empty and the agent
	// runs in the runtime's default directory instead.
	if sess.WorktreeID == "" {
		if refreshed, ok := s.provisionWorktree(r.Context(), user.ID, sess.ProjectID, sess); ok {
			sess = refreshed
		}
	}

	spec := contracts.SessionSpec{
		SessionID:     sess.ID,
		Kind:          sess.Kind,
		ProjectID:     sess.ProjectID,
		ModelRef:      sess.ModelRef,
		ReadOnlyTools: sess.Kind == contracts.KindSidecar,
	}
	// A missing worktree is not fatal for submitting a prompt: the runtime can
	// still start, and the workspace endpoints report the missing sandbox.
	if root, err := s.worktreeRoot(r.Context(), user.ID, sess); err == nil {
		spec.Worktree = root
	}

	// Starting the process and submitting the prompt are the two steps whose
	// effects must survive the client going away, so they run detached from the
	// request's cancellation (but still bounded).
	startCtx, startCancel := detach(r.Context(), 30*time.Second)
	err = s.hub.ensure(startCtx, user.ID, spec)
	startCancel()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	promptCtx, promptCancel := detach(r.Context(), 60*time.Second)
	res, err := rt.Prompt(promptCtx, contracts.PromptRequest{
		SessionID:         sess.ID,
		Text:              in.Text,
		Images:            in.Images,
		StreamingBehavior: in.StreamingBehavior,
		ClientMsgID:       clientID,
		ModelRef:          sess.ModelRef,
	})
	promptCancel()
	if err != nil {
		// Nothing was persisted, so the client may safely retry with the same
		// client_msg_id.
		s.fail(w, r, err)
		return
	}

	rec, created, err := s.deps.Store.SaveMessage(r.Context(), user.ID, sess.ID, clientID, in.Text, res)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !created {
		// Two submissions raced; the first one wins and both callers see it.
		writeJSON(w, http.StatusOK, promptResultFromRecord(rec))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleInterrupt aborts the run and returns whatever the user had queued so
// the composer can put it back (PRD FR-1.4).
func (s *server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rt, err := s.requireRuntime()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// clear_queue is accepted for compatibility; Interrupt always clears both
	// queues and reports what it dropped.
	var in struct {
		ClearQueue *bool `json:"clear_queue"`
	}
	if err := decodeOptionalJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := rt.Interrupt(r.Context(), sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if res.ClearedSteering == nil {
		res.ClearedSteering = []string{}
	}
	if res.ClearedFollowUp == nil {
		res.ClearedFollowUp = []string{}
	}
	writeJSON(w, http.StatusOK, res)
}

// promptResultFromRecord rebuilds the original response for an idempotent
// replay.
func promptResultFromRecord(rec *store.MessageRecord) contracts.PromptResult {
	return contracts.PromptResult{
		Accepted:      rec.Accepted,
		Disposition:   rec.Disposition,
		QueuePosition: rec.QueuePosition,
		RunID:         rec.RunID,
	}
}
