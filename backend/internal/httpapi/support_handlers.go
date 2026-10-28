package httpapi

import (
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// Support mode (PRD FR-16.8): an admin can read one session's transcript, for a
// bounded window, after saying why, and the owner is told.
//
// The shape matters more than the feature. Support access is *not* a bypass of the
// ownership check that every other handler uses: a mutating route would still 404
// for an admin, and nothing here can change a session. Read-only is a property of
// the endpoints, not a promise in a comment.

// handleCreateSupportAccess records the intent and returns the transcript.
func (s *server) handleCreateSupportAccess(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var in struct {
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.SessionID) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "session_id is required"))
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		// A receipt with no reason is a receipt the owner cannot judge.
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "reason is required"))
		return
	}

	access, err := s.deps.Store.CreateSupportAccess(r.Context(), user.ID, in.SessionID, reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	transcript, err := s.supportTranscript(r, in.SessionID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access":     access,
		"transcript": transcript,
	})
}

// handleSupportTranscript re-reads a session that has an unexpired grant.
func (s *server) handleSupportTranscript(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sessionID := pathID(r)
	if _, err := s.deps.Store.SupportAccessGrant(r.Context(), user.ID, sessionID); err != nil {
		s.fail(w, r, err)
		return
	}
	transcript, err := s.supportTranscript(r, sessionID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transcript": transcript})
}

// supportTranscript reads the events of a session the caller does not own. It is
// the only place in this package that does so, and it is admin-gated by its callers.
func (s *server) supportTranscript(r *http.Request, sessionID string) ([]supportLine, error) {
	sess, err := s.deps.Store.SessionAny(r.Context(), sessionID)
	if err != nil {
		return nil, err
	}
	events, err := s.exportEvents(r, sess.UserID, sessionID)
	if err != nil {
		return nil, err
	}
	lines := messagesFromEvents(events)
	out := make([]supportLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, supportLine{Role: line.role, Text: line.text})
	}
	return out, nil
}

// supportLine is one message in a support transcript.
type supportLine struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// handleSupportNotices lists the receipts owed to the signed-in user.
func (s *server) handleSupportNotices(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	notices, err := s.deps.Store.SupportNotices(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	unseen := 0
	for _, notice := range notices {
		if notice.SeenAt == nil {
			unseen++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notices": notices, "unseen": unseen})
}

// handleSupportNoticeSeen marks one receipt read.
func (s *server) handleSupportNoticeSeen(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.deps.Store.MarkSupportNoticeSeen(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}
