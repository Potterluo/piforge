package httpapi

import (
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
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
	// The provider needs the id it knows the sandbox by, and this row carries it:
	// the docker provider stores the container name (`piforge-<hash>`) as the
	// sandbox id, the local provider the pair id (`sbx_<hash>`). Re-deriving the
	// pair id here is what produced the 422 — the docker provider was asked for
	// `piforge-sbx_<hash>`. sandbox.ID stays as the fallback for a row written
	// before the id was recorded.
	providerID := sb.ID
	if strings.TrimSpace(providerID) == "" {
		providerID = sandbox.ID(sb.UserID, sb.ProjectID)
	}
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
	// Same rule as pause: the row's id is the provider's id (see above).
	resumeID := sb.ID
	if strings.TrimSpace(resumeID) == "" {
		resumeID = sandbox.ID(sb.UserID, sb.ProjectID)
	}
	live, err := s.deps.Sandbox.Resume(r.Context(), resumeID)
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

// handleSandboxSnapshots lists the snapshots recorded for a session's sandbox,
// newest first.
//
// Snapshots are written by the idle reaper before it reclaims a sandbox and by
// nothing else yet, so this endpoint is read-only by design: there is no
// restore-from-snapshot route, and inventing one before the snapshot is a real
// filesystem image would be a promise the backend cannot keep. blob_key is
// relative to the deployment's <dataDir>/snapshots directory.
func (s *server) handleSandboxSnapshots(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sandboxFor(w, r)
	if !ok {
		return
	}
	user := auth.MustUser(r.Context())
	rows, err := s.deps.Store.ListSnapshots(r.Context(), user.ID, sb.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": rows})
}

// sessionsOnSandbox lists the sessions whose worktree belongs to one sandbox.
func (s *server) sessionsOnSandbox(r *http.Request, userID, sandboxID string) []string {
	return s.sessionsOnSandboxContext(r.Context(), userID, sandboxID)
}
