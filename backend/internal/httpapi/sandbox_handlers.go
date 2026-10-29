package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// Sandbox lifecycle endpoints (PRD FR-5.2, FR-5.9).
//
// The state machine existed (creating → starting → ready → busy → idle → paused →
// terminated) and nothing could move it: `Pause` and `Resume` had no callers outside
// their tests, so a sandbox was "pausable" in the same way a light switch with no wires
// is a light switch.

// sandboxFor loads the sandbox that backs a session, through its worktree.
func (s *server) sandboxFor(w http.ResponseWriter, r *http.Request) (*contracts.Sandbox, bool) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	if sess.WorktreeID == "" {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "这个会话还没有 checkout，先发一条消息让它建起来"))
		return nil, false
	}
	wt, err := s.deps.Store.Worktree(r.Context(), user.ID, sess.WorktreeID)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	sb, err := s.deps.Store.Sandbox(r.Context(), user.ID, wt.SandboxID)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return sb, true
}

// handleSandboxState reports what the sandbox is doing.
func (s *server) handleSandboxState(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sandboxFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             sb.ID,
		"state":          sb.State,
		"message":        sb.Message,
		"expires_at":     sb.ExpiresAt,
		"last_active_at": sb.LastActiveAt,
	})
}

// handleSandboxPause stops the agents and marks the sandbox paused.
//
// Pausing stops work, so it also stops the agent processes: leaving them running would
// mean "paused" describes the label, not the machine.
func (s *server) handleSandboxPause(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sandboxFor(w, r)
	if !ok {
		return
	}
	user := auth.MustUser(r.Context())
	// The provider keys a sandbox by (user, project), not by the row's id: the row is
	// created after the directory exists, so the two ids never agreed. Passing the row id
	// answered "sandbox: not found" for a sandbox that was right there.
	providerID := sandbox.ID(sb.UserID, sb.ProjectID)
	if err := s.deps.Sandbox.Pause(r.Context(), providerID); err != nil {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "暂停失败：%v", err))
		return
	}
	// Every session on this sandbox loses its process; they restart on demand.
	for _, id := range s.sessionsOnSandbox(r, user.ID, sb.ID) {
		_ = s.deps.Runtime.Stop(r.Context(), id)
	}
	if err := s.deps.Store.UpdateSandboxState(r.Context(), user.ID, sb.ID, contracts.SandboxPaused, "手动暂停"); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": contracts.SandboxPaused})
}

// handleSandboxResume brings a paused sandbox back.
func (s *server) handleSandboxResume(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sandboxFor(w, r)
	if !ok {
		return
	}
	user := auth.MustUser(r.Context())
	live, err := s.deps.Sandbox.Resume(r.Context(), sandbox.ID(sb.UserID, sb.ProjectID))
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrSandboxUnavail, "唤醒失败：%v", err))
		return
	}
	if err := s.deps.Store.UpdateSandboxState(r.Context(), user.ID, sb.ID, live.State, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": live.State})
}

// sessionsOnSandbox lists the sessions whose worktree belongs to one sandbox.
func (s *server) sessionsOnSandbox(r *http.Request, userID, sandboxID string) []string {
	sessions, err := s.deps.Store.ListSessions(r.Context(), userID, store.SessionFilter{Limit: 200})
	if err != nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, sess := range sessions {
		if sess.WorktreeID == "" {
			continue
		}
		wt, werr := s.deps.Store.Worktree(r.Context(), userID, sess.WorktreeID)
		if werr != nil || wt.SandboxID != sandboxID {
			continue
		}
		out = append(out, sess.ID)
	}
	return out
}
