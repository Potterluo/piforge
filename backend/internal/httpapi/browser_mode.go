package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browserlease"
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

// browserScope is the resource the lease protects. The browser belongs to the
// sandbox, not the session (PRD §7.7), so two sessions of one project contend
// for the same page. Keying the lease by session would let each of them think it
// owns the browser; keying it by sandbox is what makes the lock real.
//
// A session with no checkout has no browser yet, and its own id is a sufficient
// scope until one exists.
func (s *server) browserScope(ctx context.Context, userID string, sess *contracts.Session) string {
	if sess.WorktreeID != "" {
		if wt, err := s.deps.Store.Worktree(ctx, userID, sess.WorktreeID); err == nil && wt.SandboxID != "" {
			return "sandbox:" + wt.SandboxID
		}
	}
	return "session:" + sess.ID
}

func browserOwner(user *contracts.User, sess *contracts.Session) browserlease.Owner {
	return browserlease.Owner{
		UserID:       user.ID,
		UserName:     displayNameOf(user),
		SessionID:    sess.ID,
		SessionLabel: sessionLabelOf(sess),
	}
}

// displayNameOf is what the UI prints as "正在被 X 控制". Seeded and invited
// users may have no display name, and showing a raw uuid there is worse than
// the local part of their email.
func displayNameOf(user *contracts.User) string {
	if user == nil {
		return ""
	}
	if name := strings.TrimSpace(user.DisplayName); name != "" {
		return name
	}
	if i := strings.IndexByte(user.Email, '@'); i > 0 {
		return user.Email[:i]
	}
	return user.ID
}

func sessionLabelOf(sess *contracts.Session) string {
	for _, candidate := range []string{sess.Label, sess.Title} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// effectiveBrowserMode is the mode as it actually is, not as it was last set: a
// takeover whose lease expired (closed tab, dead network) is observation again.
// Without this, a stale `takeover` entry would keep accepting input from a
// client that no longer holds the lease.
func (s *server) effectiveBrowserMode(ctx context.Context, userID string, sess *contracts.Session) browserMode {
	if browserModeOf(sess.ID) != browserModeTakeover {
		return browserModeObserve
	}
	if _, ok := s.leases.Holder(s.browserScope(ctx, userID, sess)); !ok {
		return browserModeObserve
	}
	return browserModeTakeover
}

// browserModeView is the wire shape of GET/POST browser/mode. `mode` and
// `ttl_seconds` keep the old fields; `holder` is the FR-11.3 addition and is
// present whenever somebody (possibly the caller) holds the browser.
type browserModeView struct {
	Mode       string             `json:"mode"`
	TTLSeconds int                `json:"ttl_seconds"`
	Holder     *browserHolderView `json:"holder,omitempty"`
}

type browserHolderView struct {
	UserID           string `json:"user_id"`
	UserName         string `json:"user_name"`
	SessionID        string `json:"session_id"`
	SessionLabel     string `json:"session_label,omitempty"`
	OwnedByMe        bool   `json:"owned_by_me"`
	RemainingSeconds int    `json:"remaining_seconds"`
	ExpiresAt        string `json:"expires_at"`
}

func (s *server) holderView(lease browserlease.Lease, owner browserlease.Owner, now time.Time) *browserHolderView {
	return &browserHolderView{
		UserID:           lease.Owner.UserID,
		UserName:         lease.Owner.UserName,
		SessionID:        lease.Owner.SessionID,
		SessionLabel:     lease.Owner.SessionLabel,
		OwnedByMe:        lease.Owner.Same(owner),
		RemainingSeconds: int(lease.Remaining(now).Round(time.Second) / time.Second),
		ExpiresAt:        lease.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// handleBrowserMode reads or sets who is driving the page.
func (s *server) handleBrowserMode(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	scope := s.browserScope(r.Context(), user.ID, sess)
	owner := browserOwner(user, sess)
	now := s.now()

	if r.Method == http.MethodGet {
		view := browserModeView{Mode: string(s.effectiveBrowserMode(r.Context(), user.ID, sess)), TTLSeconds: int(s.leases.TTL() / time.Second)}
		if lease, ok := s.leases.Holder(scope); ok {
			view.Holder = s.holderView(lease, owner, now)
		}
		writeJSON(w, http.StatusOK, view)
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

	mode := browserMode(in.Mode)
	if mode == browserModeObserve {
		// Switching back to observe is the explicit release (FR-11.3). It frees
		// the browser immediately instead of waiting for the TTL, so the other
		// tab's next poll can take over.
		s.leases.Release(scope, owner)
		setBrowserMode(sess.ID, mode)
		writeJSON(w, http.StatusOK, browserModeView{Mode: string(mode), TTLSeconds: int(s.leases.TTL() / time.Second)})
		return
	}

	lease, claimErr := s.leases.Claim(scope, owner)
	if claimErr != nil {
		var busy *browserlease.BusyError
		if errors.As(claimErr, &busy) {
			remaining := int(busy.Remaining.Round(time.Second) / time.Second)
			s.fail(w, r, withStatusDetails(http.StatusConflict, contracts.ErrBrowserBusy,
				browserBusyMessage(busy, remaining),
				map[string]any{
					"holder":             busy.Holder.Owner.UserName,
					"holder_user_id":     busy.Holder.Owner.UserID,
					"holder_session_id":  busy.Holder.Owner.SessionID,
					"holder_session":     busy.Holder.Owner.SessionLabel,
					"remaining_seconds":  remaining,
					"expires_at":         busy.Holder.ExpiresAt.UTC().Format(time.RFC3339),
				}))
			return
		}
		s.fail(w, r, apiErr(contracts.ErrInternal, "cannot take over the browser"))
		return
	}

	// Taking over stops the agent's own browser tools for the duration. A run that is
	// driving the same page while a person types into it produces actions nobody can
	// attribute — that is the whole reason the two modes are exclusive.
	setBrowserMode(sess.ID, mode)
	if s.deps.Runtime != nil {
		_, _ = s.deps.Runtime.Interrupt(r.Context(), sess.ID)
	}
	writeJSON(w, http.StatusOK, browserModeView{
		Mode:       string(mode),
		TTLSeconds: int(s.leases.TTL() / time.Second),
		Holder:     s.holderView(lease, owner, now),
	})
}

// browserBusyMessage names the holder and the wait in one sentence the panel
// can show as-is. The code stays machine-readable; this is the human half.
func browserBusyMessage(busy *browserlease.BusyError, remaining int) string {
	holder := busy.Holder.Owner.Label()
	if remaining <= 0 {
		return "浏览器正在被 " + holder + " 控制"
	}
	return "浏览器正在被 " + holder + " 控制，剩余 " + strconv.Itoa(remaining) + " 秒"
}
