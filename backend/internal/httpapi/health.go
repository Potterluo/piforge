package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// handleHealthz reports that the process is alive. It deliberately does not
// touch the database: a liveness probe should not restart the binary because
// the disk is briefly busy.
func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports whether the process can serve requests, which for now
// means "the database answers".
func (s *server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.Ping(r.Context()); err != nil {
		s.log.WarnContext(r.Context(), "readiness failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"reason": "database",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleNotFound keeps unknown routes in the documented error shape.
func (s *server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, apiErr(contracts.ErrNotFound, "no route for %s %s", r.Method, r.URL.Path))
}
