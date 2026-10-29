package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
)

// Test results (PRD FR-12.5).
//
// The runs are derived from the session's own events, but they are read as their
// own list: the panel asks "did the tests run, and what failed", not "what did
// the transcript say". The endpoint is a plain GET because it is a pure read of
// derived data; ownership is proved by the store's per-session scoping, and a
// foreign session is a 404 like every other cross-user access.
//
// Response shape:
//
//	{"runs": [{"id", "session_id", "source", "runner", "command", "status",
//	           "total", "passed", "failed", "skipped", "duration_ms",
//	           "failures": [{"name", "file", "line", "message"}],
//	           "output_tail", "started_at", "ended_at"}], "count": N}
//
// status is one of running | passed | failed | unrecognized. `unrecognized`
// carries output_tail (the raw last few lines) and no counts: the command was a
// recognised runner's, but its output could not be read.
func (s *server) handleTestRuns(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sessionID := pathID(r)

	limit := queryInt(r, "limit", 20)
	runs, err := s.deps.Store.ListTestRuns(r.Context(), user.ID, sessionID, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// ListTestRuns always allocates its result, so this is [] rather than null
	// on the wire; the panel can render the empty state from a successful 200.
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":  runs,
		"count": len(runs),
	})
}
