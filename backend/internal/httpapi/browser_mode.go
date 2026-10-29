package httpapi

import (
	"net/http"
	"sync"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// Browser control modes (PRD FR-11.2, FR-11.4).
//
// Watching and driving are different intents, and the difference has to be enforced
// somewhere. It is enforced here, in the socket's input path, not in the panel: a client
// that forgets to check the mode must not be able to type into a page the user is only
// watching. The default is observe.
type browserMode string

const (
	browserModeObserve  browserMode = "observe"
	browserModeTakeover browserMode = "takeover"
)

var (
	browserModesMu sync.RWMutex
	// browserModes is keyed by session id and lives in memory on purpose: control is
	// about who is holding the wheel right now, and a restart means nobody is.
	browserModes = map[string]browserMode{}
)

func browserModeOf(sessionID string) browserMode {
	browserModesMu.RLock()
	defer browserModesMu.RUnlock()
	if mode, ok := browserModes[sessionID]; ok {
		return mode
	}
	return browserModeObserve
}

func setBrowserMode(sessionID string, mode browserMode) {
	browserModesMu.Lock()
	defer browserModesMu.Unlock()
	if mode == "" || mode == browserModeObserve {
		delete(browserModes, sessionID)
		return
	}
	browserModes[sessionID] = mode
}

// handleBrowserMode reads or sets who is driving the page.
func (s *server) handleBrowserMode(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"mode": string(browserModeOf(sess.ID))})
		return
	}

	var in struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	switch browserMode(in.Mode) {
	case browserModeObserve, browserModeTakeover:
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "mode must be observe or takeover"))
		return
	}

	// Taking over stops the agent's own browser tools for the duration. A run that is
	// driving the same page while a person types into it produces actions nobody can
	// attribute — that is the whole reason the two modes are exclusive.
	mode := browserMode(in.Mode)
	setBrowserMode(sess.ID, mode)
	if mode == browserModeTakeover {
		if s.deps.Runtime != nil {
			_, _ = s.deps.Runtime.Interrupt(r.Context(), sess.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": string(mode)})
}
