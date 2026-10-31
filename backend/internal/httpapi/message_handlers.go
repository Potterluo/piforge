package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
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
	// ReviewComment turns this message into "a comment on one line" (FR-12.3).
	// The text is composed server-side from path:line, the quoted code and the
	// comment, so the transcript, the agent's prompt and the stored message are
	// one string instead of three that can disagree.
	ReviewComment *ReviewComment `json:"review_comment,omitempty"`
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
	// FR-12.3: a comment on a line is sent through this same endpoint. Its text is
	// composed here, before the empty-text check, because the client sends the
	// anchor (path, line, quote) rather than a prompt.
	if in.ReviewComment != nil {
		composed, cerr := s.reviewPrompt(r.Context(), user.ID, sess, *in.ReviewComment)
		if cerr != nil {
			s.fail(w, r, cerr)
			return
		}
		in.Text = composed
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Images) == 0 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "text or images are required"))
		return
	}
	// FR-5.2: using a session wakes its sandbox. This is the second place it can happen
	// (the first is provisioning a new checkout), and it is the one that matters for a
	// session that already existed when the sandbox was paused.
	s.wakeSandbox(r, user.ID, sess)

	switch in.StreamingBehavior {
	case "", "steer", "follow_up":
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"streaming_behavior must be empty, steer or follow_up"))
		return
	}

	// FR-3.1–3.3: decide who gets to run before this run starts. A branch sending a
	// prompt pauses its running siblings unless parallel mode is on; parallel mode
	// refuses past the limit with a reason instead of a bare 429.
	paused, allowed := s.enforceConcurrency(w, r, user.ID, sess)
	if !allowed {
		return
	}
	if len(paused) > 0 {
		s.recordPausedSiblings(r, user.ID, sess, paused)
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

	// A session pinned to a model that cannot answer must fail here, before a
	// process starts and before Pi sees the prompt.
	//
	// This is the acceptance pass's keyless trap: picking `shared-anthropic` in
	// the new-session dialog wrote `model_ref = shared-anthropic/...` and the
	// send was accepted, so the gateway answered 502 provider_unavailable and Pi
	// retried it three times (~30s) before a half-raw error reached the
	// transcript. `explainMissingModel` already knew how to say this; it only ran
	// after a failure, which never happened because nothing had tried yet.
	//
	// Only an explicit reference is checked. An empty model_ref follows the
	// deployment default, which the first-run bar and the composer already
	// explain; resolving it here would make a fake-agent deployment with a
	// keyless default refuse every send (and the e2e suite runs exactly that).
	if strings.TrimSpace(sess.ModelRef) != "" {
		if modelErr := s.explainMissingModel(r.Context(), user.ID, sess.ModelRef); modelErr != nil {
			s.fail(w, r, modelErr)
			return
		}
	}

	// The first prompt for a session should run inside its checkout. Best
	// effort: a provisioning failure leaves spec.Worktree empty and the agent
	// runs in the runtime's default directory instead.
	if sess.WorktreeID == "" {
		if refreshed, ok := s.provisionWorktree(r.Context(), user.ID, sess.ProjectID, sess); ok {
			sess = refreshed
		}
	}

	// One builder for every spec: the composer path and the runtime-start path
	// must agree about the worktree and the mounted skills, or an agent started
	// after a model switch loses the skill grant the first one had.
	spec := s.sessionSpec(user.ID, sess)

	// The composer's model picker writes the session row; a live process keeps
	// the model it resolved at startup, so the two have to be reconciled before
	// the next turn or the picker is decoration (QA-14, MAJOR).
	if _, err := s.restartForModelSwitch(r.Context(), user.ID, sess); err != nil {
		s.fail(w, r, err)
		return
	}

	// Starting the process and submitting the prompt are the two steps whose
	// effects must survive the client going away, so they run detached from the
	// request's cancellation (but still bounded).
	startCtx, startCancel := detach(r.Context(), 30*time.Second)
	err = s.hub.ensure(startCtx, user.ID, spec)
	startCancel()
	if err != nil {
		// Starting the agent is the other place a missing model shows up: with no
		// provider the child cannot be told which model to use and exits. Same
		// rule as below — explain configuration when it is the cause.
		if modelErr := s.explainMissingModel(r.Context(), user.ID, sess.ModelRef); modelErr != nil {
			s.fail(w, r, modelErr)
			return
		}
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
		// A failure here is usually config, not a bug: no provider, a deleted
		// provider, a provider with no models. Saying "internal error" over a
		// prompt that cannot work sends the user to the wrong place, so the
		// configuration check decides the message. Nothing was persisted either
		// way, so the client may retry with the same client_msg_id.
		if modelErr := s.explainMissingModel(r.Context(), user.ID, sess.ModelRef); modelErr != nil {
			s.fail(w, r, modelErr)
			return
		}
		s.fail(w, r, err)
		return
	}

	// The new-session dialog promises 留空则用第一条消息自动命名. A session that keeps
	// a placeholder title is listed, forked and shared under a name that says
	// nothing, so the first message names it.
	s.autoTitle(r.Context(), user.ID, sess, in.Text)

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

// explainMissingModel turns a failed prompt into an actionable message when the
// cause is configuration rather than a bug.
//
// It stays quiet when it cannot be sure: an unnamed model falls back to the
// deployment default, which this layer does not know, and inventing an error
// there would hide the real one.
func (s *server) explainMissingModel(ctx context.Context, userID, modelRef string) error {
	if s.deps.Store == nil {
		return nil
	}
	providerName, modelID := modelParts(modelRef)
	if providerName == "" {
		// No explicit model: the deployment default applies, exactly as the
		// runtime resolves it. A session that pins nothing is the normal case for
		// a first admin, so guessing "maybe it will work" here is what produced
		// "internal error" on a fresh deployment.
		providerName = s.deps.DefaultProvider
		if providerName == "" {
			return apiErr(contracts.ErrNoModel,
				"no model provider is configured: add one in Settings → Models")
		}
	}
	provider, _, err := s.deps.Store.ResolveProvider(ctx, userID, providerName)
	if err != nil || provider == nil {
		return apiErr(contracts.ErrNoModel,
			"the provider %q is not configured: add it in Settings → Models", providerName)
	}
	if len(provider.Models) == 0 && modelID == "" {
		return apiErr(contracts.ErrNoModel,
			"the provider %q lists no models: add a model id in Settings → Models", providerName)
	}
	// A provider that resolves but has no key fails every run at the first model
	// call, and the agent reports that as an opaque "internal error". The
	// inherited default provider is exactly this shape on a fresh deployment,
	// which is how an admin with nothing configured ends up here.
	if !provider.HasKey {
		return apiErr(contracts.ErrNoModel,
			"the provider %q has no API key: add one in Settings → Models", providerName)
	}
	return nil
}

// autoTitle names a session after its first message when the user left the title
// empty. Anything the user typed is kept as-is.
func (s *server) autoTitle(ctx context.Context, userID string, sess *contracts.Session, text string) {
	if sess == nil || strings.TrimSpace(sess.Title) != "" {
		return
	}
	title := firstLine(text)
	if title == "" {
		return
	}
	updated, err := s.deps.Store.UpdateSession(ctx, userID, sess.ID, store.SessionPatch{Title: &title})
	if err != nil {
		// A title is a convenience; failing to write one must never fail the send.
		return
	}
	*sess = *updated
}

// firstLine takes the first non-empty line of a prompt, trimmed to a length that
// fits a sidebar row.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 40 {
			return string(runes[:40]) + "…"
		}
		return line
	}
	return ""
}

// modelParts splits "provider/model". An empty reference has no provider: the
// caller has to fall back to the deployment default, which this layer does not
// know, so "unset" stays distinguishable from "named".
func modelParts(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ""
	}
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		return provider, model
	}
	return ref, ""
}

// recordPausedSiblings leaves a visible trace when a run stops another run (FR-3.1).
//
// A session that simply goes quiet is indistinguishable from one that died, so the
// event log says who paused it.
func (s *server) recordPausedSiblings(r *http.Request, userID string, sess *contracts.Session, paused []string) {
	ctx := context.WithoutCancel(r.Context())
	for _, id := range paused {
		event := &contracts.Event{
			SessionID: id,
			RunID:     "",
			Type:      "session.paused",
			Payload:   json.RawMessage(`{"reason":"another branch started running","by":"` + sess.ID + `"}`),
			TS:        time.Now(),
		}
		if _, err := s.deps.Store.AppendEvent(ctx, userID, event); err != nil {
			s.log.WarnContext(r.Context(), "could not record a paused sibling", "session", id, "error", err.Error())
		}
	}
}

// wakeSandbox resumes a paused sandbox before a message runs.
//
// A pause is meant to free resources, not to make the session unusable: the user's next
// message is an unambiguous request to use it. A sandbox that is missing or terminated is
// left alone — waking it is not something a message can do.
func (s *server) wakeSandbox(r *http.Request, userID string, sess *contracts.Session) {
	if sess.WorktreeID == "" || s.deps.Sandbox == nil {
		return
	}
	wt, err := s.deps.Store.Worktree(r.Context(), userID, sess.WorktreeID)
	if err != nil {
		return
	}
	sb, err := s.deps.Store.Sandbox(r.Context(), userID, wt.SandboxID)
	if err != nil {
		return
	}
	if sb.State == contracts.SandboxPaused {
		live, rerr := s.deps.Sandbox.Resume(r.Context(), sandbox.ID(sb.UserID, sb.ProjectID))
		if rerr != nil {
			return
		}
		_ = s.deps.Store.UpdateSandboxState(r.Context(), userID, sb.ID, live.State, "")
		return
	}
	// A message is the clearest real activity a sandbox has. Without this touch,
	// last_active_at would remain the creation time and the idle reaper (FR-5.3)
	// would pause a sandbox somebody is actively using.
	switch sb.State {
	case contracts.SandboxReady, contracts.SandboxBusy, contracts.SandboxIdle:
		if terr := s.deps.Store.TouchSandbox(r.Context(), userID, sb.ID); terr != nil {
			s.log.WarnContext(r.Context(), "could not record sandbox activity",
				"sandbox", sb.ID, "error", terr.Error())
		}
	}
}
