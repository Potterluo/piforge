package httpapi

// The recovery surface after a run ends without a result.
//
// There are three ways that happens — the user pressed 停止, the control plane
// restarted, the agent child died — and until now the transcript showed one
// sentence and stopped there. This file turns the event log into two answers a
// person can act on:
//
//   - GET /v1/sessions/{id}/recovery: the cause, the sentence, the last request,
//     and what the checkout actually holds (dirty count, last commit), or that
//     there is nothing left to continue;
//   - POST /messages with continue_interrupted: the follow-up is composed here,
//     server-side, so the transcript, the stored message and the agent's prompt
//     are one string. It is never sent automatically.
//
// The facts come from the places that can prove them. The cause is read from the
// interruption event (an old session's sentence is matched, an unrecognised one
// stays unknown). Dirty count and HEAD are read from git. A sandbox that is gone
// is reported as gone: the one thing this surface must not do is offer a 继续
// button that cannot work.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/recovery"
	"github.com/piforge/piforge/backend/internal/store"
)

// recoverySurvivalView is the measured checkout, in wire form.
type recoverySurvivalView struct {
	// Summary is the sentence the marker shows. It is composed server-side so a
	// client cannot turn "unknown" into "clean".
	Summary string `json:"summary"`
	// Unavailable is "", "sandbox" or "workspace".
	Unavailable string `json:"unavailable,omitempty"`
	DirtyKnown  bool   `json:"dirty_known"`
	DirtyCount  int    `json:"dirty_count"`
	CommitKnown bool   `json:"commit_known"`
	LastCommit  string `json:"last_commit,omitempty"`
}

// recoveryView is GET /v1/sessions/{id}/recovery.
type recoveryView struct {
	Interrupted   bool                 `json:"interrupted"`
	Cause         string               `json:"cause,omitempty"`
	CauseLabel    string               `json:"cause_label,omitempty"`
	Reason        string               `json:"reason,omitempty"`
	LastRequest   string               `json:"last_request,omitempty"`
	InterruptedAt string               `json:"interrupted_at,omitempty"`
	Survival      recoverySurvivalView `json:"survival"`
	CanContinue   bool                 `json:"can_continue"`
	// BlockedReason says why 继续 is not offered. An absent action with no
	// sentence is the same dead end this feature removes.
	BlockedReason string `json:"blocked_reason,omitempty"`

	// measured is the same survival facts in the form ComposeContinue takes.
	measured recovery.Survival
}

// handleSessionRecovery answers what a person who came back to an interrupted
// session needs to know before deciding what to type.
func (s *server) handleSessionRecovery(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.sessionRecovery(r.Context(), user.ID, sess)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// sessionRecovery builds the view. It never errors for "not interrupted": that
// is an answer, not a failure.
func (s *server) sessionRecovery(ctx context.Context, userID string, sess *contracts.Session) (*recoveryView, error) {
	rec, err := s.deps.Store.LastInterruption(ctx, userID, sess.ID)
	if errors.Is(err, store.ErrNotFound) {
		return &recoveryView{Interrupted: false}, nil
	}
	if err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(rec.Reason)
	if reason == "" {
		// The record exists but carries no sentence. Saying "被中断" without a
		// cause is honest; inventing one of the three would not be.
		reason = "本次运行被中断"
	}
	measured := s.measureSurvival(ctx, userID, sess)
	view := &recoveryView{
		Interrupted:   true,
		Cause:         rec.Cause,
		CauseLabel:    recovery.CauseLabel(rec.Cause),
		Reason:        reason,
		LastRequest:   rec.LastRequest,
		InterruptedAt: rec.TS.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Survival: recoverySurvivalView{
			Summary:     measured.Summary(),
			Unavailable: measured.Unavailable,
			DirtyKnown:  measured.DirtyKnown,
			DirtyCount:  measured.DirtyCount,
			CommitKnown: measured.CommitKnown,
			LastCommit:  measured.LastCommit,
		},
		measured: measured,
	}

	switch {
	case rec.Continued:
		view.BlockedReason = "这次中断已经继续过了"
	case strings.TrimSpace(rec.LastRequest) == "":
		view.BlockedReason = "无法从事件记录确定上次请求，无法自动续接"
	case measured.Unavailable == recovery.UnavailableSandbox:
		view.BlockedReason = "沙箱已不存在，无法继续；请新建一个会话"
	case measured.Unavailable == recovery.UnavailableWorkspace:
		view.BlockedReason = "工作区已不存在，无法继续；请新建一个会话"
	case sess.Status == contracts.StatusArchived:
		view.BlockedReason = "会话已归档，无法继续"
	}
	view.CanContinue = view.BlockedReason == ""
	return view, nil
}

// measureSurvival reads the checkout, distinguishing "not measured" from
// "measured empty".
//
// The sandbox row is the authority for "is the checkout still there". A
// MISSING/TERMINATED sandbox (the idle reaper destroys the workspace after
// snapshotting) is gone; reporting a dirty count for a directory that no longer
// exists would be the exact lie this feature is meant to remove.
func (s *server) measureSurvival(ctx context.Context, userID string, sess *contracts.Session) recovery.Survival {
	if sess.WorktreeID == "" {
		return recovery.Survival{Unavailable: recovery.UnavailableWorkspace}
	}
	wt, err := s.deps.Store.Worktree(ctx, userID, sess.WorktreeID)
	if errors.Is(err, store.ErrNotFound) {
		return recovery.Survival{Unavailable: recovery.UnavailableWorkspace}
	}
	if err != nil {
		// A store failure is "unknown", not "gone".
		return recovery.Survival{}
	}
	if sb, serr := s.deps.Store.Sandbox(ctx, userID, wt.SandboxID); serr == nil && sb != nil {
		switch sb.State {
		case contracts.SandboxMissing, contracts.SandboxTerminated:
			return recovery.Survival{Unavailable: recovery.UnavailableSandbox}
		}
	} else if errors.Is(serr, store.ErrNotFound) {
		return recovery.Survival{Unavailable: recovery.UnavailableSandbox}
	}
	if s.deps.Workspace == nil {
		return recovery.Survival{}
	}

	out := recovery.Survival{}
	if status, err := s.deps.Workspace.Status(ctx, wt.Path); err == nil && status != nil {
		out.DirtyKnown = true
		out.DirtyCount = len(status.Changes)
	}
	if graph, err := s.deps.Workspace.Log(ctx, wt.Path, 1, ""); err == nil && graph != nil {
		out.CommitKnown = true
		if graph.Head != "" {
			out.HasCommit = true
			out.LastCommit = shortCommitHash(graph.Head)
		}
	}
	return out
}

// shortCommitHash is the seven-character form the marker shows. It is a display
// detail, not a revision argument: nothing resolves it back to a commit.
func shortCommitHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// interruptedTurnNote is the sentence the control plane puts in front of the
// first message after an interruption.
//
// It exists because of a measured product failure, not a theory: after 停止, the
// next message was answered against the interrupted request ("Say beta." came
// back with the 1–200 list, 6/6 runs). The message reached the model and the run
// was attributed correctly — the agent still had the truncated answer in its
// conversation and continued it, or re-ran the earlier request. Pi owns that
// history and offers no way to edit a persisted entry, so the control plane
// marks the interruption in the prompt instead. Written in the product's
// language because the transcript shows it: the user is looking at the sentence
// the agent was given.
//
// Measured effect (same sequence, deployment): 0/6 answered the new message
// without it; 5/5 did with this wording (a weaker wording: 4/5). It is a strong
// mitigation, not a guarantee — the model can still decide to finish the
// interrupted task, and the sentence then tells the user why.
const interruptedTurnNote = "[上一轮已被用户中断并作废：不要继续它，也不要重新执行上一轮的请求。只回答下面这条消息。]"

// interruptedTurnNote returns the sentence to prefix, or "" when the session's
// last thing is not an open interruption.
//
// "Open" means no run has started since: once the user sends anything, the
// interruption is history and repeating the sentence on every later message
// would be noise the user cannot remove.
func (s *server) interruptedTurnNote(ctx context.Context, userID, sessionID string) string {
	if s.deps.Store == nil {
		return ""
	}
	rec, err := s.deps.Store.LastInterruption(ctx, userID, sessionID)
	if err != nil || rec == nil || rec.Continued {
		return ""
	}
	return interruptedTurnNote
}

// continuePrompt composes the 继续 message for an interrupted session.
//
// The client sends only the intent (continue_interrupted); the text is built
// here from the event log and the checkout. That is what makes the transcript
// show exactly what the agent received, instead of a prompt the browser guessed.
func (s *server) continuePrompt(ctx context.Context, userID string, sess *contracts.Session) (string, error) {
	view, err := s.sessionRecovery(ctx, userID, sess)
	if err != nil {
		return "", err
	}
	if !view.Interrupted {
		return "", withStatus(http.StatusConflict, contracts.ErrConflict,
			"这次运行没有被中断，没有可以继续的工作")
	}
	if !view.CanContinue {
		return "", withStatus(http.StatusConflict, contracts.ErrConflict, view.BlockedReason)
	}
	text, err := recovery.ComposeContinue(view.LastRequest,
		recovery.Interruption{Cause: view.Cause, Reason: view.Reason}, view.measured)
	if err != nil {
		return "", apiErr(contracts.ErrConflict, "无法组成继续消息：%v", err)
	}
	return text, nil
}
