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
	"github.com/piforge/piforge/backend/internal/recovery"
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
	// ContinueInterrupted asks the server to compose the follow-up that the
	// 继续 action on an interruption marker sends. The text is built here from
	// the event log (the last request, the cause) and the measured checkout, so
	// the transcript shows exactly what the agent received. It is a deliberate
	// request: nothing sends it on its own.
	ContinueInterrupted bool `json:"continue_interrupted,omitempty"`
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
	// anchor (path, line, quote) rather than a prompt. The composer also reports
	// what its size caps cut, so the response can say it instead of leaving the
	// caller to discover it by reading the composed text.
	var reviewTruncation *contracts.ReviewTruncation
	if in.ReviewComment != nil {
		if in.ContinueInterrupted {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
				"continue_interrupted and review_comment cannot be combined"))
			return
		}
		composed, truncated, cerr := s.reviewPrompt(r.Context(), user.ID, sess, *in.ReviewComment)
		if cerr != nil {
			s.fail(w, r, cerr)
			return
		}
		in.Text = composed
		reviewTruncation = truncated
	}
	// A run that ended without a result has one deliberate recovery action: the
	// server composes a follow-up from the event log (what was asked, why the run
	// stopped) and the measured checkout, then sends it through the ordinary
	// message path. The client never supplies the text, so the transcript cannot
	// disagree with what the agent received.
	if in.ContinueInterrupted {
		if strings.TrimSpace(in.Text) != "" {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
				"continue_interrupted composes its text on the server; text must be empty"))
			return
		}
		composed, cerr := s.continuePrompt(r.Context(), user.ID, sess)
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
	// The agent keeps the interrupted turn in its conversation: Pi persists the
	// truncated assistant message, and the next request is answered against that
	// half-written answer instead of the new message (HANDOFF §2, reproduced
	// 5/5 on the deployment). The control plane cannot edit Pi's session, so it
	// says so in the prompt — the same server-side composition the review-comment
	// and 继续 paths use, which is why the transcript shows exactly what the agent
	// received. Only the first message after the interruption carries it: once a
	// run starts, the interruption is history.
	if !in.ContinueInterrupted {
		if note := s.interruptedTurnNote(r.Context(), user.ID, sess.ID); note != "" {
			if strings.TrimSpace(in.Text) == "" {
				in.Text = note
			} else {
				in.Text = note + "\n\n" + in.Text
			}
		}
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

	// Replay: return exactly what the first submission produced. The truncation
	// report is recomputed above from the same request, so a retry does not lose
	// the counts the first answer carried.
	if rec, err := s.deps.Store.Message(r.Context(), user.ID, sess.ID, clientID); err == nil {
		replayed := promptResultFromRecord(rec)
		replayed.ReviewTruncation = reviewTruncation
		writeJSON(w, http.StatusOK, replayed)
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

	// A model reference that no longer resolves is re-resolved here, before a
	// process starts and before Pi sees the prompt; a provider that exists but
	// cannot answer still fails here with its specific reason.
	//
	// This is where the acceptance pass's dead session is revived: its provider
	// had been deleted and re-created, the stored reference still named the old
	// one, and nothing re-read the provider list. A conversation must survive an
	// operator rotating configuration — the alternative is that every open
	// session has to be created again.
	//
	// The keyless trap is unchanged: picking `shared-anthropic` in the
	// new-session dialog still fails before the agent is touched, instead of
	// costing three retries and a raw 502.
	resolved, modelErr := s.resolveSessionModel(r.Context(), user.ID, sess)
	if modelErr != nil {
		s.fail(w, r, modelErr)
		return
	}
	if resolved.FallbackFrom != "" && resolved.Ref != sess.ModelRef {
		s.log.WarnContext(r.Context(), "session model re-resolved to a working provider",
			"session", sess.ID, "from", resolved.FallbackFrom, "to", resolved.Ref)
		s.pinSessionModel(r.Context(), user.ID, sess, resolved.Ref)
	}

	// The first prompt for a session should run inside its checkout. Best
	// effort: a provisioning failure leaves spec.Worktree empty and the agent
	// runs in the runtime's default directory instead. A *refusal* is different:
	// the tenant has no sandbox left under its quota, and that is the answer to
	// this request, not a detail to log.
	if sess.WorktreeID == "" {
		refreshed, perr := s.provisionWorktree(r.Context(), user.ID, sess.ProjectID, sess)
		if perr != nil {
			if isPolicyRefusal(perr) {
				s.fail(w, r, perr)
				return
			}
		} else {
			sess = refreshed
		}
	}

	// One builder for every spec: the composer path and the runtime-start path
	// must agree about the worktree and the mounted skills, or an agent started
	// after a model switch loses the skill grant the first one had.
	spec := s.sessionSpec(user.ID, sess)

	// FR-5.8: a checkout over its disk budget must stop here, before an agent
	// process starts working in it. The measurement is a du-style walk of the
	// sandbox root (cheap and periodic), not per-write accounting; the refusal
	// names the budget, the current usage and what to delete.
	if quotaErr := s.enforceSandboxQuota(r.Context(), user.ID, sess); quotaErr != nil {
		s.fail(w, r, quotaErr)
		return
	}

	// The composer's model picker writes the session row; a live process keeps
	// the model it resolved at startup, so the two have to be reconciled before
	// the next turn or the picker is decoration (QA-14, MAJOR).
	if _, err := s.restartForModelSwitch(r.Context(), user.ID, sess); err != nil {
		s.fail(w, r, err)
		return
	}

	// A process spawned while the provider was missing keeps that empty model
	// configuration for its whole life. Now that a model resolves again, the old
	// process is the only thing left between the session and a working run.
	if _, err := s.restartForUnconfiguredAgent(r.Context(), user.ID, sess); err != nil {
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

	// Tell the client which model this turn used when it was not the one on the
	// session row, so the transcript and the header cannot disagree with what
	// the agent actually ran on.
	if resolved.FallbackFrom != "" {
		res.ModelRef = sess.ModelRef
		res.ModelFallbackFrom = resolved.FallbackFrom
	}
	// Same rule for the comment caps: the caller is told what the composer cut.
	res.ReviewTruncation = reviewTruncation

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

// stopIsInFlight reports whether a run row is still started for this session.
//
// The session row alone is not enough: it can lag the run by a moment, and a
// stop that arrives after the run completed would otherwise write an
// interruption marker over a successful turn. Both must agree.
func (s *server) stopIsInFlight(ctx context.Context, userID string, sess *contracts.Session) bool {
	if sess.Status != contracts.StatusRunning {
		return false
	}
	runs, err := s.deps.Store.ListRuns(ctx, userID, sess.ID)
	if err != nil {
		return false
	}
	for _, run := range runs {
		if run.Status == "started" {
			return true
		}
	}
	return false
}

// recordStop writes the user-stop interruption marker.
//
// A stop is not an error, so it does not go through the reconciler: the control
// plane is healthy and the run settled normally. This event is only about the
// cause, which the terminal event cannot express. The append survives the client
// navigating away (detached, bounded), and a failure is logged rather than
// surfaced: the interrupt already happened.
func (s *server) recordStop(r *http.Request, userID, sessionID string) {
	payload, err := json.Marshal(map[string]any{
		"cause":  recovery.CauseStop,
		"reason": recovery.ReasonStop,
	})
	if err != nil {
		return
	}
	ctx, cancel := detach(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.deps.Store.AppendEvent(ctx, userID, &contracts.Event{
		SessionID: sessionID,
		Type:      contracts.EvRunInterrupted,
		Payload:   payload,
		TS:        s.now(),
	}); err != nil {
		s.log.WarnContext(r.Context(), "stop marker not recorded",
			"session", sessionID, "error", err.Error())
	}
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
	// Whether the stop is about an actual in-flight run is decided before the
	// abort: a click that lands just after the run finished must not leave a
	// false "interrupted" marker in a transcript whose run completed.
	stopping := s.stopIsInFlight(r.Context(), user.ID, sess)
	res, err := rt.Interrupt(r.Context(), sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Record why the run stopped. run.settled{aborted} already tells the
	// transcript that a stop happened, but not that it was the user's decision:
	// the recovery marker names the cause, and without this event a stop would
	// be indistinguishable from a run that simply ended. Best effort — the stop
	// itself succeeded and must not fail because a marker could not be written.
	if stopping {
		s.recordStop(r, user.ID, sess.ID)
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
		// The row carries the provider's id (the container name for a container
		// provider); re-deriving the pair id here is the same mistake the pause
		// handler had (docs/EXECUTION-BOUNDARY.md §14.4).
		resumeID := sb.ID
		if strings.TrimSpace(resumeID) == "" {
			resumeID = sandbox.ID(sb.UserID, sb.ProjectID)
		}
		live, rerr := s.deps.Sandbox.Resume(r.Context(), resumeID)
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
