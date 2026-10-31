package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// sharedSandboxSession creates a second session in the same project and attaches
// it to the first session's worktree, which is what a second tab of one project
// looks like to the browser layer: same sandbox, same Chrome, different session.
func sharedSandboxSession(t *testing.T, env *testEnv, userID string, projectID, firstSessionID string) *contracts.Session {
	t.Helper()
	ctx := context.Background()
	first, err := env.store.Session(ctx, userID, firstSessionID)
	if err != nil {
		t.Fatalf("load first session: %v", err)
	}
	if first.WorktreeID == "" {
		t.Fatalf("first session has no worktree")
	}
	second, err := env.store.CreateSession(ctx, userID, store.NewSession{
		ProjectID: projectID, Kind: contracts.KindMain, Title: "second tab", ModelRef: "shared/gpt",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	wt := first.WorktreeID
	if _, err := env.store.UpdateSession(ctx, userID, second.ID, store.SessionPatch{WorktreeID: &wt}); err != nil {
		t.Fatalf("attach the shared worktree: %v", err)
	}
	return second
}

type modeResponse struct {
	Mode       string `json:"mode"`
	TTLSeconds int    `json:"ttl_seconds"`
	Holder     *struct {
		UserID           string `json:"user_id"`
		UserName         string `json:"user_name"`
		SessionID        string `json:"session_id"`
		OwnedByMe        bool   `json:"owned_by_me"`
		RemainingSeconds int    `json:"remaining_seconds"`
	} `json:"holder"`
}

type errResponse struct {
	Error *contracts.APIError `json:"error"`
}

// FR-11.3: takeover is exclusive per sandbox. The second session is refused with
// a typed browser_busy that names the holder and the wait; switching back to
// observe releases the browser so the other one can take it.
func TestBrowserTakeoverIsExclusiveAndReleases(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, first := env.mkSessionFor(user.ID, "browser")
	env.withWorktree(user.ID, project.ID, first.ID)
	second := sharedSandboxSession(t, env, user.ID, project.ID, first.ID)

	// The first takeover is granted, and it says who holds it.
	taken := env.doAuthed(http.MethodPost, "/v1/sessions/"+first.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c)
	if taken.Code != http.StatusOK {
		t.Fatalf("first takeover: %d %s", taken.Code, taken.Body.String())
	}
	var firstView modeResponse
	env.mustJSON(taken, &firstView)
	if firstView.Mode != "takeover" || firstView.Holder == nil || !firstView.Holder.OwnedByMe {
		t.Fatalf("takeover view = %+v", firstView)
	}

	// The second session's panel shows somebody else is driving, and its attempt
	// is refused by name.
	var observed modeResponse
	env.mustJSON(env.doAuthed(http.MethodGet, "/v1/sessions/"+second.ID+"/browser/mode", nil, c), &observed)
	if observed.Mode != "observe" {
		t.Fatalf("second session mode = %q, want observe while somebody else drives", observed.Mode)
	}
	if observed.Holder == nil || observed.Holder.OwnedByMe || observed.Holder.UserName == "" {
		t.Fatalf("second session holder = %+v", observed.Holder)
	}
	if observed.Holder.SessionID != first.ID {
		t.Fatalf("holder session = %q, want %q", observed.Holder.SessionID, first.ID)
	}

	refused := env.doAuthed(http.MethodPost, "/v1/sessions/"+second.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c)
	if refused.Code != http.StatusConflict {
		t.Fatalf("second takeover: %d %s", refused.Code, refused.Body.String())
	}
	var failure errResponse
	env.mustJSON(refused, &failure)
	if failure.Error == nil || failure.Error.Code != contracts.ErrBrowserBusy {
		t.Fatalf("error = %+v", failure.Error)
	}
	details := failure.Error.Details
	if details["holder"] != observed.Holder.UserName {
		t.Fatalf("details holder = %v, want %q", details["holder"], observed.Holder.UserName)
	}
	if details["holder_session_id"] != first.ID {
		t.Fatalf("details holder_session_id = %v", details["holder_session_id"])
	}
	if remaining, ok := details["remaining_seconds"].(float64); !ok || remaining <= 0 {
		t.Fatalf("details remaining_seconds = %v, want a positive number", details["remaining_seconds"])
	}
	if failure.Error.Message == "" || failure.Error.Message == "browser_busy: " {
		t.Fatalf("the refusal must explain itself: %q", failure.Error.Message)
	}

	// Switching back to observe is the explicit release; the second session can
	// then take over without waiting for a TTL.
	released := env.doAuthed(http.MethodPost, "/v1/sessions/"+first.ID+"/browser/mode",
		map[string]any{"mode": "observe"}, c)
	if released.Code != http.StatusOK {
		t.Fatalf("release: %d %s", released.Code, released.Body.String())
	}
	retaken := env.doAuthed(http.MethodPost, "/v1/sessions/"+second.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c)
	if retaken.Code != http.StatusOK {
		t.Fatalf("takeover after release: %d %s", retaken.Code, retaken.Body.String())
	}
	var retakenView modeResponse
	env.mustJSON(retaken, &retakenView)
	if retakenView.Holder == nil || retakenView.Holder.SessionID != second.ID || !retakenView.Holder.OwnedByMe {
		t.Fatalf("holder after release = %+v", retakenView.Holder)
	}
}

// FR-11.3: a lease that stops being renewed expires. The old panel cannot keep
// typing after its TTL, and the mode it left behind must not read as control.
func TestBrowserTakeoverExpires(t *testing.T) {
	env := newTestEnv(t)
	clock := &fakeHTTPClock{at: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}
	env.deps.Now = clock.Now
	env.deps.BrowserTakeoverTTL = 30 * time.Second
	env.rebuild()

	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, first := env.mkSessionFor(user.ID, "browser")
	env.withWorktree(user.ID, project.ID, first.ID)
	second := sharedSandboxSession(t, env, user.ID, project.ID, first.ID)

	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+first.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c); rec.Code != http.StatusOK {
		t.Fatalf("takeover: %d %s", rec.Code, rec.Body.String())
	}

	clock.Advance(20 * time.Second)
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+second.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c); rec.Code != http.StatusConflict {
		t.Fatalf("takeover before the TTL: %d %s", rec.Code, rec.Body.String())
	}

	clock.Advance(11 * time.Second)
	var stale modeResponse
	env.mustJSON(env.doAuthed(http.MethodGet, "/v1/sessions/"+first.ID+"/browser/mode", nil, c), &stale)
	if stale.Mode != "observe" {
		t.Fatalf("expired session still reports %q", stale.Mode)
	}
	if stale.Holder != nil {
		t.Fatalf("an expired lease still has a holder: %+v", stale.Holder)
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+second.ID+"/browser/mode",
		map[string]any{"mode": "takeover"}, c); rec.Code != http.StatusOK {
		t.Fatalf("takeover after the TTL: %d %s", rec.Code, rec.Body.String())
	}
}

// fakeHTTPClock is the Deps.Now clock for expiry tests; the lease manager and
// the handlers must read the same one or the test would only prove the manager.
type fakeHTTPClock struct {
	at time.Time
}

func (c *fakeHTTPClock) Now() time.Time { return c.at }

func (c *fakeHTTPClock) Advance(d time.Duration) { c.at = c.at.Add(d) }
