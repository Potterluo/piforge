package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid argument", apiErr(contracts.ErrInvalidArgument, "x"), 400, contracts.ErrInvalidArgument},
		{"unauthenticated", apiErr(contracts.ErrUnauthenticated, "x"), 401, contracts.ErrUnauthenticated},
		{"forbidden", apiErr(contracts.ErrForbidden, "x"), 403, contracts.ErrForbidden},
		{"not found", apiErr(contracts.ErrNotFound, "x"), 404, contracts.ErrNotFound},
		{"session busy", apiErr(contracts.ErrSessionBusy, "x"), 409, contracts.ErrSessionBusy},
		{"need full resync", apiErr(contracts.ErrNeedFullResync, "x"), 409, contracts.ErrNeedFullResync},
		{"branch conflict", apiErr(contracts.ErrBranchConflict, "x"), 409, contracts.ErrBranchConflict},
		{"conflict", apiErr(contracts.ErrConflict, "x"), 409, contracts.ErrConflict},
		{"file too large", apiErr(contracts.ErrFileTooLarge, "x"), 413, contracts.ErrFileTooLarge},
		{"sandbox unavailable", apiErr(contracts.ErrSandboxUnavail, "x"), 422, contracts.ErrSandboxUnavail},
		{"sandbox paused", apiErr(contracts.ErrSandboxPaused, "x"), 423, contracts.ErrSandboxPaused},
		{"rate limited", apiErr(contracts.ErrRateLimited, "x"), 429, contracts.ErrRateLimited},
		{"upstream overloaded", apiErr(contracts.ErrUpstreamOverload, "x"), 503, contracts.ErrUpstreamOverload},
		{"internal", apiErr(contracts.ErrInternal, "x"), 500, contracts.ErrInternal},
		{"unknown code", apiErr("no_such_code", "x"), 500, "no_such_code"},
		{"store not found", store.ErrNotFound, 404, contracts.ErrNotFound},
		{"store forbidden", store.ErrForbidden, 403, contracts.ErrForbidden},
		{"store conflict", store.ErrConflict, 409, contracts.ErrConflict},
		{"store invalid", store.ErrInvalid, 400, contracts.ErrInvalidArgument},
		{"explicit 503", withStatus(503, contracts.ErrSandboxUnavail, "x"), 503, contracts.ErrSandboxUnavail},
		{"wrapped api error", wrap(apiErr(contracts.ErrNotFound, "x")), 404, contracts.ErrNotFound},
		// A client disconnect is not a server fault: 499 keeps it out of the
		// error budget and out of the 5xx logs.
		{"client cancelled", context.Canceled, statusClientClosed, contracts.ErrClientClosed},
		{"client deadline", context.DeadlineExceeded, statusClientClosed, contracts.ErrClientClosed},
		{"plain error", errors.New("boom"), 500, contracts.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := classify(tc.err)
			if status != tc.status {
				t.Fatalf("status = %d, want %d", status, tc.status)
			}
			if body.Code != tc.code {
				t.Fatalf("code = %q, want %q", body.Code, tc.code)
			}
			if body.Message == "" {
				t.Fatal("empty message")
			}
		})
	}
}

// wrap simulates an error from a lower layer that got context added.
func wrap(err error) error { return &wrapped{err} }

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "context: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

func TestWriteErrWireShape(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(http.MethodGet, "/v1/projects", nil, nil, "")
	env.mustErr(rec, http.StatusUnauthorized, contracts.ErrUnauthenticated)
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
	// The wire object is {"error": {"code", "message"}}, because
	// web/lib/types.ts reads body.error.code.
	var raw map[string]any
	env.mustJSON(rec, &raw)
	inner, ok := raw["error"].(map[string]any)
	if !ok {
		t.Fatalf("body is not wrapped in an error envelope: %v", raw)
	}
	if inner["code"] != contracts.ErrUnauthenticated {
		t.Fatalf("code = %v", inner["code"])
	}
	if _, ok := inner["message"].(string); !ok {
		t.Fatalf("message missing: %v", inner)
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealthEndpoints(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(http.MethodGet, "/healthz", nil, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	rec = env.do(http.MethodGet, "/readyz", nil, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d", rec.Code)
	}

	// Unknown routes answer with the API error shape, not text/plain.
	rec = env.do(http.MethodGet, "/v1/nope", nil, nil, "")
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func TestLoginMeLogout(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "piforge-dev-123", contracts.RoleUser)

	c := env.login("dev@piforge.local", "piforge-dev-123")
	if c.user.ID != user.ID {
		t.Fatalf("login user = %q", c.user.ID)
	}

	// Both cookies are set, and the CSRF header duplicates the readable token.
	var haveSession, haveCSRF bool
	for _, ck := range c.cookies {
		switch ck.Name {
		case "pf_session":
			haveSession = ck.HttpOnly
		case "pf_csrf":
			haveCSRF = !ck.HttpOnly && ck.Value == c.csrf
		}
	}
	if !haveSession || !haveCSRF {
		t.Fatalf("cookies = %+v", c.cookies)
	}

	rec := env.do(http.MethodGet, "/v1/auth/me", nil, c, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d", rec.Code)
	}
	var me struct {
		User *contracts.User `json:"user"`
		CSRF string          `json:"csrf"`
	}
	env.mustJSON(rec, &me)
	if me.User.Email != "dev@piforge.local" || me.CSRF == "" {
		t.Fatalf("me = %s", rec.Body.String())
	}

	// The client types logout as {ok: boolean}, so it must not be a 204.
	rec = env.doAuthed(http.MethodPost, "/v1/auth/logout", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d", rec.Code)
	}
	var ok map[string]bool
	env.mustJSON(rec, &ok)
	if !ok["ok"] {
		t.Fatalf("logout body = %s", rec.Body.String())
	}
	// The session is gone from the server, not just from the browser.
	if rec := env.do(http.MethodGet, "/v1/auth/me", nil, c, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", rec.Code)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "right", contracts.RoleUser)

	cases := []struct{ email, password string }{
		{"dev@piforge.local", "wrong"},
		{"ghost@piforge.local", "right"},
	}
	for _, tc := range cases {
		rec := env.do(http.MethodPost, "/v1/auth/login",
			map[string]string{"email": tc.email, "password": tc.password}, nil, "")
		env.mustErr(rec, http.StatusUnauthorized, contracts.ErrUnauthenticated)
	}

	disabled := env.mkUser("off@piforge.local", "right", contracts.RoleUser)
	_ = env.store.SetUserStatus(context.Background(), disabled.ID, "disabled")
	rec := env.do(http.MethodPost, "/v1/auth/login",
		map[string]string{"email": "off@piforge.local", "password": "right"}, nil, "")
	env.mustErr(rec, http.StatusForbidden, contracts.ErrForbidden)
}

func TestCSRFIsRequiredForMutations(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	body := map[string]string{"name": "repo"}

	// No header.
	rec := env.do(http.MethodPost, "/v1/projects", body, c, "")
	env.mustErr(rec, http.StatusForbidden, contracts.ErrForbidden)

	// Wrong header.
	rec = env.do(http.MethodPost, "/v1/projects", body, c, "not-the-token")
	env.mustErr(rec, http.StatusForbidden, contracts.ErrForbidden)

	// Correct header.
	rec = env.do(http.MethodPost, "/v1/projects", body, c, c.csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with csrf = %d body %s", rec.Code, rec.Body.String())
	}

	// Reads never need the header.
	if rec := env.do(http.MethodGet, "/v1/projects", nil, c, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET with csrf = %d", rec.Code)
	}
	// And an unauthenticated mutation is 401 before it is 403.
	if rec := env.do(http.MethodPost, "/v1/projects", body, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Projects and sessions
// ---------------------------------------------------------------------------

func TestProjectEnvelopes(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{
		"name": "repo", "repo_url": "https://example.com/r.git", "default_branch": "main",
	}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Project *contracts.Project `json:"project"`
	}
	env.mustJSON(rec, &created)
	if created.Project == nil || created.Project.Name != "repo" {
		t.Fatalf("body = %s", rec.Body.String())
	}

	rec = env.doAuthed(http.MethodGet, "/v1/projects", nil, c)
	var listed struct {
		Projects []*contracts.Project `json:"projects"`
	}
	env.mustJSON(rec, &listed)
	if len(listed.Projects) != 1 {
		t.Fatalf("projects = %d", len(listed.Projects))
	}

	rec = env.doAuthed(http.MethodGet, "/v1/projects/"+created.Project.ID, nil, c)
	var got struct {
		Project *contracts.Project `json:"project"`
	}
	env.mustJSON(rec, &got)
	if got.Project.ID != created.Project.ID {
		t.Fatalf("project id = %q", got.Project.ID)
	}

	// Validation: an empty name is derived, not refused. With no repository the
	// name is the dated blank workspace, so the project is still addressable.
	rec = env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{"name": "  "}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("blank name = %d body %s", rec.Code, rec.Body.String())
	}
	var blank struct {
		Project *contracts.Project `json:"project"`
	}
	env.mustJSON(rec, &blank)
	if blank.Project == nil || !strings.HasPrefix(blank.Project.Name, "空白工作区 · ") {
		t.Fatalf("blank project = %s", rec.Body.String())
	}

	// Duplicate name is a conflict.
	rec = env.doAuthed(http.MethodPost, "/v1/projects", map[string]string{"name": "repo"}, c)
	env.mustErr(rec, http.StatusConflict, contracts.ErrConflict)
}

func TestSessionLifecycle(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, _ := env.mkSessionFor(user.ID, "repo")

	// Create a second session through the API.
	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+project.ID+"/sessions", map[string]string{
		"kind": "main", "title": "second", "model_ref": "shared/gpt",
	}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session = %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &created)
	if created.Session.Kind != contracts.KindMain || created.Session.Title != "second" {
		t.Fatalf("session = %+v", created.Session)
	}

	// List: envelope plus newest-first ordering.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions", nil, c)
	var list struct {
		Sessions   []*contracts.Session `json:"sessions"`
		NextCursor string               `json:"next_cursor"`
	}
	env.mustJSON(rec, &list)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions = %d", len(list.Sessions))
	}

	// Detail carries the runtime status and the project.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+created.Session.ID, nil, c)
	var detail struct {
		Session *contracts.Session      `json:"session"`
		Status  contracts.RuntimeStatus `json:"status"`
		Project *contracts.Project      `json:"project"`
	}
	env.mustJSON(rec, &detail)
	if detail.Session.ID != created.Session.ID {
		t.Fatalf("detail session = %q", detail.Session.ID)
	}
	if !detail.Status.Running {
		t.Fatalf("status = %+v", detail.Status)
	}
	if detail.Project.ID != project.ID {
		t.Fatalf("project = %q", detail.Project.ID)
	}

	// Patch.
	rec = env.doAuthed(http.MethodPatch, "/v1/sessions/"+created.Session.ID,
		map[string]string{"title": "renamed", "status": "archived"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d body %s", rec.Code, rec.Body.String())
	}
	var patched struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &patched)
	if patched.Session.Title != "renamed" || patched.Session.Status != contracts.StatusArchived {
		t.Fatalf("patched = %+v", patched.Session)
	}

	// Status endpoint is flat.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+created.Session.ID+"/status", nil, c)
	var status contracts.RuntimeStatus
	env.mustJSON(rec, &status)
	if status.SessionID != created.Session.ID || !status.Running {
		t.Fatalf("status = %+v", status)
	}

	// Stats is flat.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+created.Session.ID+"/stats", nil, c)
	var stats contracts.SessionStats
	env.mustJSON(rec, &stats)
	if stats.SessionID != created.Session.ID {
		t.Fatalf("stats = %+v", stats)
	}

	// Paging.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions?limit=1", nil, c)
	var page1 struct {
		Sessions   []*contracts.Session `json:"sessions"`
		NextCursor string               `json:"next_cursor"`
	}
	env.mustJSON(rec, &page1)
	if len(page1.Sessions) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v", page1)
	}
	rec = env.doAuthed(http.MethodGet, "/v1/sessions?limit=1&cursor="+page1.NextCursor, nil, c)
	var page2 struct {
		Sessions []*contracts.Session `json:"sessions"`
	}
	env.mustJSON(rec, &page2)
	if len(page2.Sessions) != 1 || page2.Sessions[0].ID == page1.Sessions[0].ID {
		t.Fatalf("page2 = %+v", page2)
	}
}

// TestCrossUserAccessIs404 is the security test: another user's project and
// session must be indistinguishable from a missing one.
func TestCrossUserAccessIs404(t *testing.T) {
	env := newTestEnv(t)
	alice := env.mkUser("alice@example.com", "pw", contracts.RoleUser)
	env.mkUser("bob@example.com", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(alice.ID, "alice-repo")

	bob := env.login("bob@example.com", "pw")

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/projects/" + project.ID, nil},
		{http.MethodGet, "/v1/sessions/" + session.ID, nil},
		{http.MethodPatch, "/v1/sessions/" + session.ID, map[string]string{"title": "x"}},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/status", nil},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/stats", nil},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/entries", nil},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/tree", nil},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/fork-points", nil},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/events", nil},
		{http.MethodPost, "/v1/sessions/" + session.ID + "/messages", map[string]string{"client_msg_id": "c1", "text": "hi"}},
		{http.MethodPost, "/v1/sessions/" + session.ID + "/interrupt", map[string]any{}},
		{http.MethodPost, "/v1/sessions/" + session.ID + "/fork", map[string]string{"entry_id": "e1"}},
		{http.MethodPost, "/v1/sessions/" + session.ID + "/sidecar", map[string]any{}},
		{http.MethodPost, "/v1/sessions/" + session.ID + "/compact", map[string]any{}},
		{http.MethodGet, "/v1/sessions/" + session.ID + "/files/tree", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := env.doAuthed(tc.method, tc.path, tc.body, bob)
			env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
		})
	}

	// Bob's own listings stay empty.
	rec := env.doAuthed(http.MethodGet, "/v1/projects", nil, bob)
	var listed struct {
		Projects []*contracts.Project `json:"projects"`
	}
	env.mustJSON(rec, &listed)
	if len(listed.Projects) != 0 {
		t.Fatalf("bob sees %d projects", len(listed.Projects))
	}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func TestPostMessageIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	body := map[string]any{"client_msg_id": "cm_1", "text": "hello"}
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", body, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("message = %d body %s", rec.Code, rec.Body.String())
	}
	var first contracts.PromptResult
	env.mustJSON(rec, &first)
	if !first.Accepted || first.RunID != "run_scripted" {
		t.Fatalf("result = %+v", first)
	}
	if env.runtime.promptCount() != 1 {
		t.Fatalf("prompt count = %d", env.runtime.promptCount())
	}

	// Replay: same answer, no second prompt.
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", body, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay = %d body %s", rec.Code, rec.Body.String())
	}
	var second contracts.PromptResult
	env.mustJSON(rec, &second)
	if second != first {
		t.Fatalf("replay = %+v, want %+v", second, first)
	}
	if env.runtime.promptCount() != 1 {
		t.Fatalf("replay re-prompted: count = %d", env.runtime.promptCount())
	}
	// The replay did not (re)start the agent either.
	if env.runtime.startCount() != 1 {
		t.Fatalf("replay restarted the agent: starts = %d", env.runtime.startCount())
	}

	// A different id prompts again. The fake runtime closes its event channel
	// after the script, so the hub sees a stopped session and starts it again —
	// in production the channel stays open and the subscription is reused.
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_2", "text": "again"}, c)
	if rec.Code != http.StatusOK || env.runtime.promptCount() != 2 {
		t.Fatalf("second message = %d count = %d", rec.Code, env.runtime.promptCount())
	}
	if env.runtime.startCount() < 1 {
		t.Fatal("the agent was never started")
	}
}

func TestPostMessageRequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	rec := env.do(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hello"}, c, "")
	env.mustErr(rec, http.StatusForbidden, contracts.ErrForbidden)
	if env.runtime.promptCount() != 0 {
		t.Fatal("a rejected request must not reach the agent")
	}
}

func TestPostMessageValidation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	path := "/v1/sessions/" + sess.ID + "/messages"

	cases := []map[string]any{
		{"text": "no client id"},
		{"client_msg_id": "c1"},
		{"client_msg_id": "c2", "text": "hi", "streaming_behavior": "whenever"},
	}
	for _, body := range cases {
		rec := env.doAuthed(http.MethodPost, path, body, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	}
}

func TestPostMessageMapsSessionBusy(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	env.runtime.promptErr = contracts.NewError(contracts.ErrSessionBusy, "a run is in flight")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	env.mustErr(rec, http.StatusConflict, contracts.ErrSessionBusy)

	// A failed prompt must not be remembered as the idempotent result.
	env.runtime.mu.Lock()
	env.runtime.promptErr = nil
	env.runtime.mu.Unlock()
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d body %s", rec.Code, rec.Body.String())
	}
}

// TestEventPumpPersistsRuntimeEvents proves the runtime channel is drained into
// the store, which is what makes SSE replay work at all.
func TestEventPumpPersistsRuntimeEvents(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	env.runtime.script = []contracts.Event{
		{Type: contracts.EvRunStarted, RunID: "run_scripted", Payload: json.RawMessage(`{"trigger":"user"}`)},
		{Type: contracts.EvMsgDelta, Payload: json.RawMessage(`{"chunk":"he"}`)},
		{Type: contracts.EvRunSettled, RunID: "run_scripted", Payload: json.RawMessage(`{"tokens_in":10}`)},
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("message = %d body %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, err := env.store.ListEvents(context.Background(), user.ID, sess.ID, 0, 100)
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if len(events) >= 3 {
			if events[0].Seq != 1 || events[2].Seq != 3 {
				t.Fatalf("seqs = %d..%d", events[0].Seq, events[2].Seq)
			}
			// The pump also derives the run row from run.started/settled.
			runs, err := env.store.ListRuns(context.Background(), user.ID, sess.ID)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(runs) != 1 || runs[0].TokensIn != 10 || runs[0].SettledAt == nil {
				t.Fatalf("runs = %+v", runs)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runtime events were never persisted")
}

func TestInterrupt(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	env.runtime.interrupt = contracts.InterruptResult{
		ClearedSteering: []string{"stop"},
		ClearedFollowUp: []string{"next"},
	}
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/interrupt",
		map[string]bool{"clear_queue": true}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("interrupt = %d body %s", rec.Code, rec.Body.String())
	}
	var res contracts.InterruptResult
	env.mustJSON(rec, &res)
	if len(res.ClearedSteering) != 1 || len(res.ClearedFollowUp) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// ---------------------------------------------------------------------------
// Runtime-backed read endpoints
// ---------------------------------------------------------------------------

func TestRuntimeEnvelopes(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")
	base := "/v1/sessions/" + sess.ID

	rec := env.doAuthed(http.MethodGet, base+"/entries?since=", nil, c)
	var entries struct {
		Entries []*contracts.TreeEntry `json:"entries"`
		LeafID  string                 `json:"leaf_id"`
	}
	env.mustJSON(rec, &entries)
	if len(entries.Entries) != 1 || entries.LeafID != "e1" {
		t.Fatalf("entries = %s", rec.Body.String())
	}

	rec = env.doAuthed(http.MethodGet, base+"/tree", nil, c)
	var tree struct {
		Tree   []*contracts.TreeEntry `json:"tree"`
		LeafID string                 `json:"leaf_id"`
	}
	env.mustJSON(rec, &tree)
	if len(tree.Tree) != 1 || tree.LeafID != "e1" {
		t.Fatalf("tree = %s", rec.Body.String())
	}

	rec = env.doAuthed(http.MethodGet, base+"/fork-points", nil, c)
	var points struct {
		Points []contracts.ForkPoint `json:"points"`
	}
	env.mustJSON(rec, &points)
	if len(points.Points) != 1 {
		t.Fatalf("points = %s", rec.Body.String())
	}

	rec = env.doAuthed(http.MethodPost, base+"/compact",
		map[string]string{"custom_instructions": "keep the diff"}, c)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("compact = %d body %s", rec.Code, rec.Body.String())
	}
	var okBody map[string]bool
	env.mustJSON(rec, &okBody)
	if !okBody["ok"] {
		t.Fatalf("compact body = %s", rec.Body.String())
	}
	// The key web/lib/api/client.ts sends must reach the runtime.
	if len(env.runtime.compaction) != 1 || env.runtime.compaction[0] != "keep the diff" {
		t.Fatalf("compaction = %+v", env.runtime.compaction)
	}
}

// TestSettingsEndpoints covers the skills/MCP bodies exactly as
// web/lib/api/client.ts sends them, including content-hash derivation.
func TestSettingsEndpoints(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	t.Run("skills", func(t *testing.T) {
		rec := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
			"name": "review", "description": "code review", "body": "# SKILL",
		}, c)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create skill = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Skill *contracts.Skill `json:"skill"`
		}
		env.mustJSON(rec, &body)
		if body.Skill.Name != "review" || body.Skill.ContentHash == "" {
			t.Fatalf("skill = %+v", body.Skill)
		}
		// The hash now covers the published bundle (path + content), not just a
		// body string, because a skill is a directory: two bundles with the same
		// SKILL.md but different assets are different skills.
		if body.Skill.BundleKey == "" {
			t.Fatal("a skill created with a body has no bundle key")
		}
		first := body.Skill.ContentHash
		again := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
			"name": "review-2", "description": "same body", "body": "# SKILL",
		}, c)
		if again.Code != http.StatusCreated {
			t.Fatalf("second create = %d body %s", again.Code, again.Body.String())
		}
		var second struct {
			Skill *contracts.Skill `json:"skill"`
		}
		env.mustJSON(again, &second)
		if second.Skill.ContentHash != first {
			t.Fatalf("identical bundles hashed differently: %q vs %q", first, second.Skill.ContentHash)
		}
		// Leave the list assertion below with a single skill.
		if rec := env.doAuthed(http.MethodDelete, "/v1/skills/"+second.Skill.ID, nil, c); rec.Code >= 300 {
			t.Fatalf("cleanup delete = %d", rec.Code)
		}

		rec = env.doAuthed(http.MethodGet, "/v1/skills", nil, c)
		var list struct {
			Skills []*contracts.Skill `json:"skills"`
		}
		env.mustJSON(rec, &list)
		if len(list.Skills) != 1 {
			t.Fatalf("skills = %d", len(list.Skills))
		}

		rec = env.doAuthed(http.MethodDelete, "/v1/skills/"+body.Skill.ID, nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("delete skill = %d", rec.Code)
		}
	})

	t.Run("mcp servers", func(t *testing.T) {
		rec := env.doAuthed(http.MethodPost, "/v1/mcp/servers", map[string]any{
			"scope": "user", "name": "fs", "transport": "stdio",
			"command": "npx", "args": []string{"-y", "server-fs"},
		}, c)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create mcp = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Server *contracts.MCPServer `json:"server"`
		}
		env.mustJSON(rec, &body)
		if body.Server.Name != "fs" || len(body.Server.Args) != 2 {
			t.Fatalf("server = %+v", body.Server)
		}
		if body.Server.HasSecrets {
			t.Fatal("secrets must not be reported as stored until the vault exists")
		}

		rec = env.doAuthed(http.MethodGet, "/v1/mcp/servers", nil, c)
		var list struct {
			Servers []*contracts.MCPServer `json:"servers"`
		}
		env.mustJSON(rec, &list)
		if len(list.Servers) != 1 {
			t.Fatalf("servers = %d", len(list.Servers))
		}

		// A user cannot create a shared entry from this route.
		rec = env.doAuthed(http.MethodPost, "/v1/mcp/servers", map[string]any{
			"scope": "admin", "name": "sneaky", "transport": "stdio", "command": "x",
		}, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)

		rec = env.doAuthed(http.MethodDelete, "/v1/mcp/servers/"+body.Server.ID, nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("delete mcp = %d", rec.Code)
		}
	})
}

// sha256Hex mirrors the server-side derivation for the skills test.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestForkCreatesBranch(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/fork",
		map[string]any{"entry_id": "e1", "label": "alt"}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork = %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &created)
	if created.Session.Kind != contracts.KindBranch ||
		created.Session.ParentID != sess.ID ||
		created.Session.RootID != sess.ID ||
		created.Session.ForkEntryID != "e1" ||
		created.Session.Label != "alt" {
		t.Fatalf("branch = %+v", created.Session)
	}
	if len(env.runtime.forked) != 1 || env.runtime.forked[0].NewSession != created.Session.ID {
		t.Fatalf("fork request = %+v", env.runtime.forked)
	}
	if created.Session.ProjectID != project.ID {
		t.Fatalf("branch project = %q", created.Session.ProjectID)
	}

	// entry_id is mandatory: Pi can only fork at a user message.
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/fork", map[string]any{}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
}

func TestSidecarSharesWorktreeAndIsCapped(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")

	// Give the parent a worktree so sharing can be observed.
	ctx := context.Background()
	sb, err := env.store.CreateSandbox(ctx, user.ID, store.NewSandbox{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, user.ID, store.NewWorktree{
		SandboxID: sb.ID, Path: "/srv/wt/repo", Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, user.ID, sess.ID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}

	for i := 0; i < 3; i++ {
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/sidecar",
			map[string]any{"model_ref": "shared/cheap"}, c)
		if rec.Code != http.StatusCreated {
			t.Fatalf("sidecar %d = %d body %s", i, rec.Code, rec.Body.String())
		}
		var created struct {
			Session *contracts.Session `json:"session"`
		}
		env.mustJSON(rec, &created)
		if created.Session.Kind != contracts.KindSidecar {
			t.Fatalf("kind = %q", created.Session.Kind)
		}
		if created.Session.WorktreeID != wt.ID {
			t.Fatalf("sidecar worktree = %q, want the parent's %q", created.Session.WorktreeID, wt.ID)
		}
		if created.Session.ModelRef != "shared/cheap" {
			t.Fatalf("sidecar model = %q", created.Session.ModelRef)
		}
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/sidecar", map[string]any{}, c)
	env.mustErr(rec, http.StatusConflict, contracts.ErrConflict)
}

// ---------------------------------------------------------------------------
// Providers and admin
// ---------------------------------------------------------------------------

func TestProvidersMergeAndShadow(t *testing.T) {
	env := newTestEnv(t)
	admin := env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)

	// Two shared providers, one of which the user will shadow by name.
	for _, name := range []string{"shared", "other"} {
		if _, err := env.store.UpsertAdminProvider(context.Background(), admin.ID, store.ProviderInput{
			Name: name, API: "openai-compat", Models: []contracts.ModelSpec{{ID: "big"}},
			KeyCipher: []byte("cipher"),
		}); err != nil {
			t.Fatalf("UpsertAdminProvider(%s): %v", name, err)
		}
	}

	c := env.login("dev@piforge.local", "pw")
	rec := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "shared", "api": "openai-compat", "base_url": "https://mine",
		"api_key": "sk-local", "models": []map[string]any{{"id": "cheap"}},
	}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create provider = %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Provider *contracts.Provider `json:"provider"`
	}
	env.mustJSON(rec, &created)
	if !created.Provider.HasKey || created.Provider.Source != contracts.SourceCustom {
		t.Fatalf("provider = %+v", created.Provider)
	}

	rec = env.doAuthed(http.MethodGet, "/v1/providers", nil, c)
	var merged struct {
		Providers []*contracts.Provider `json:"providers"`
	}
	env.mustJSON(rec, &merged)
	if len(merged.Providers) != 2 {
		t.Fatalf("providers = %+v", merged.Providers)
	}
	byName := map[string]*contracts.Provider{}
	for _, p := range merged.Providers {
		byName[p.Name] = p
	}
	if byName["shared"].Source != contracts.SourceCustom || byName["shared"].BaseURL != "https://mine" {
		t.Fatalf("shadowing failed: %+v", byName["shared"])
	}
	if byName["other"].Source != contracts.SourceInherited {
		t.Fatalf("inherited = %+v", byName["other"])
	}

	// Deleting the shared one is impossible through this route.
	rec = env.doAuthed(http.MethodDelete, "/v1/providers/"+byName["other"].ID, nil, c)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)

	rec = env.doAuthed(http.MethodDelete, "/v1/providers/"+created.Provider.ID, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUsersRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)

	admin := env.login("admin@piforge.local", "pw")
	rec := env.doAuthed(http.MethodGet, "/v1/admin/users", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin users = %d", rec.Code)
	}
	var listed struct {
		Users []*contracts.User `json:"users"`
	}
	env.mustJSON(rec, &listed)
	if len(listed.Users) != 2 {
		t.Fatalf("users = %d", len(listed.Users))
	}

	dev := env.login("dev@piforge.local", "pw")
	rec = env.doAuthed(http.MethodGet, "/v1/admin/users", nil, dev)
	env.mustErr(rec, http.StatusForbidden, contracts.ErrForbidden)
}

// ---------------------------------------------------------------------------
// Workspace endpoints
// ---------------------------------------------------------------------------

// withWorktree gives a session a sandbox + worktree and returns the path.
func (e *testEnv) withWorktree(userID, projectID, sessionID string) string {
	e.t.Helper()
	ctx := context.Background()
	sb, err := e.store.CreateSandbox(ctx, userID, store.NewSandbox{ProjectID: projectID})
	if err != nil {
		e.t.Fatalf("CreateSandbox: %v", err)
	}
	path := "/srv/wt/" + projectID
	wt, err := e.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID, Path: path, Branch: "main",
	})
	if err != nil {
		e.t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := e.store.UpdateSession(ctx, userID, sessionID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		e.t.Fatalf("attach worktree: %v", err)
	}
	return path
}

func TestWorkspaceEndpoints(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")
	root := env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	t.Run("file tree", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/files/tree?path=&depth=1", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Nodes []*contracts.FileNode `json:"nodes"`
		}
		env.mustJSON(rec, &body)
		if len(body.Nodes) != 1 || body.Nodes[0].Name != "main.go" {
			t.Fatalf("nodes = %+v", body.Nodes)
		}
		if got := env.workspace.lastRoot(); got != root {
			t.Fatalf("workspace root = %q, want %q", got, root)
		}
	})

	t.Run("read file", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/files?path=main.go", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		var body contracts.FileContent
		env.mustJSON(rec, &body)
		if body.Path != "main.go" || body.Body == "" {
			t.Fatalf("content = %+v", body)
		}
	})

	t.Run("download streams raw bytes", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/files?path=main.go&download=1", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "main.go") {
			t.Fatalf("content disposition = %q", got)
		}
		if rec.Body.String() != "package main" {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})

	t.Run("download streams binary bytes without the text preview", func(t *testing.T) {
		// The preview decode fails for this file (as /v1/file/read does for a
		// PNG); a download must not touch it.
		png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0xfe}
		env.workspace.fileBytes = png
		env.workspace.readErr = errors.New("'utf-8' codec can't decode byte 0x89")
		defer func() {
			env.workspace.fileBytes = nil
			env.workspace.readErr = nil
		}()

		rec := env.doAuthed(http.MethodGet, base+"/files?path=icon.png&download=1", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("content type = %q", got)
		}
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(png)) {
			t.Fatalf("content length = %q, want %d", got, len(png))
		}
		if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="icon.png"` {
			t.Fatalf("content disposition = %q", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), png) {
			t.Fatalf("body = %x, want %x", rec.Body.Bytes(), png)
		}
		if got := rec.Header().Get("X-PF-Truncated"); got != "" {
			t.Fatalf("download was marked truncated: %q", got)
		}
	})

	t.Run("missing path is a 400", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/files", nil, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})

	t.Run("write file takes a raw body", func(t *testing.T) {
		rec := env.doRaw(http.MethodPut, base+"/files?path=notes.txt", "text/plain",
			[]byte("hello"), c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		if string(env.workspace.written["notes.txt"]) != "hello" {
			t.Fatalf("written = %q", env.workspace.written["notes.txt"])
		}
	})

	t.Run("delete file", func(t *testing.T) {
		rec := env.doAuthed(http.MethodDelete, base+"/files?path=notes.txt", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		if len(env.workspace.deleted) != 1 {
			t.Fatalf("deleted = %+v", env.workspace.deleted)
		}
	})

	t.Run("search", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/search?q=package&type=content", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Results []contracts.SearchResult `json:"results"`
		}
		env.mustJSON(rec, &body)
		if len(body.Results) != 1 {
			t.Fatalf("results = %+v", body.Results)
		}
		// A bad type is rejected rather than silently defaulted.
		rec = env.doAuthed(http.MethodGet, base+"/search?q=x&type=everything", nil, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})

	t.Run("git status", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/git/status", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body contracts.GitStatus
		env.mustJSON(rec, &body)
		if body.Branch != "main" {
			t.Fatalf("status = %+v", body)
		}
	})

	t.Run("git diff", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/git/diff?scope=staged", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Diffs []contracts.FileDiff `json:"diffs"`
		}
		env.mustJSON(rec, &body)
		if len(body.Diffs) != 1 {
			t.Fatalf("diffs = %+v", body.Diffs)
		}
		rec = env.doAuthed(http.MethodGet, base+"/git/diff?scope=nonsense", nil, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})

	t.Run("git log", func(t *testing.T) {
		rec := env.doAuthed(http.MethodGet, base+"/git/log?limit=10", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body contracts.GitGraph
		env.mustJSON(rec, &body)
		if body.Head != "abc123" {
			t.Fatalf("graph = %+v", body)
		}
	})

	t.Run("git stage and unstage", func(t *testing.T) {
		rec := env.doAuthed(http.MethodPost, base+"/git/stage",
			map[string]any{"paths": []string{"main.go"}}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("stage = %d body %s", rec.Code, rec.Body.String())
		}
		rec = env.doAuthed(http.MethodPost, base+"/git/unstage",
			map[string]any{"paths": []string{"main.go"}}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("unstage = %d body %s", rec.Code, rec.Body.String())
		}
		// Empty paths is a client error.
		rec = env.doAuthed(http.MethodPost, base+"/git/stage", map[string]any{"paths": []string{}}, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})

	t.Run("git commit", func(t *testing.T) {
		rec := env.doAuthed(http.MethodPost, base+"/git/commit", map[string]any{
			"message": "feat: x", "paths": []string{"main.go"},
		}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("commit = %d body %s", rec.Code, rec.Body.String())
		}
		var body map[string]string
		env.mustJSON(rec, &body)
		if body["commit"] != "deadbeef" {
			t.Fatalf("commit body = %v", body)
		}
		rec = env.doAuthed(http.MethodPost, base+"/git/commit", map[string]any{}, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})

	t.Run("git branch and checkout", func(t *testing.T) {
		rec := env.doAuthed(http.MethodPost, base+"/git/branch", map[string]string{"name": "fix"}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("branch = %d body %s", rec.Code, rec.Body.String())
		}
		rec = env.doAuthed(http.MethodPost, base+"/git/checkout", map[string]string{"name": "fix"}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("checkout = %d body %s", rec.Code, rec.Body.String())
		}
		if len(env.workspace.created) != 1 || len(env.workspace.co) != 1 {
			t.Fatalf("created = %+v checkout = %+v", env.workspace.created, env.workspace.co)
		}
		rec = env.doAuthed(http.MethodPost, base+"/git/branch", map[string]string{}, c)
		env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	})
}

func TestWorkspaceWithoutWorktreeIs422(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/files/tree", nil, c)
	env.mustErr(rec, http.StatusUnprocessableEntity, contracts.ErrSandboxUnavail)
}

func TestWorkspaceMissingServiceIs503(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")

	// Rebuild the router without a workspace service, leaving the worktree in
	// place so the failure can only come from the missing dependency.
	env.withWorktree(user.ID, project.ID, sess.ID)
	deps := Deps{
		Store:  env.store,
		Auth:   env.auth,
		Logger: env.logger(),
	}
	handler := NewRouter(deps)
	c := env.login("dev@piforge.local", "pw")

	req := newTestRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/files/tree", c)
	rec := serve(handler, req)
	env.mustErr(rec, http.StatusServiceUnavailable, contracts.ErrSandboxUnavail)
}

// TestProviderKeysAreSealedAtRest is the guarantee PRD §5.7 FR-13.3 makes: the
// key the user types is never stored in a readable form, and the value the
// gateway later opens is exactly what they typed.
func TestProviderKeysAreSealedAtRest(t *testing.T) {
	env := newTestEnv(t)
	dev := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	const key = "sk-ant-api03-super-secret-value"
	rec := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "mine", "api": "anthropic-messages", "api_key": key,
	}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create provider = %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Fatal("the create response echoed the API key back")
	}

	// The store hands back ciphertext; the plaintext must not appear in it.
	provider, sealed, err := env.store.ResolveProvider(context.Background(), dev.ID, "mine")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if len(sealed) == 0 {
		t.Fatal("no key was stored")
	}
	if bytes.Contains(sealed, []byte(key)) {
		t.Fatalf("the key is stored in the clear: %q", sealed)
	}
	if provider.API != "anthropic-messages" || !provider.HasKey {
		t.Fatalf("provider = %+v", provider)
	}

	// The gateway can still open it, which is the other half of the promise.
	opened, err := env.deps.Secrets.OpenString(sealed)
	if err != nil {
		t.Fatalf("open sealed key: %v", err)
	}
	if opened != key {
		t.Fatalf("opened key = %q, want the original", opened)
	}
}

// TestProviderWriteRefusedWithoutASecretBox pins the failure mode: a server
// with no secret box must reject the write rather than persist a raw key.
func TestProviderWriteRefusedWithoutASecretBox(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	without := env.deps
	without.Secrets = nil
	handler := NewRouter(without)

	body, err := json.Marshal(map[string]any{"name": "mine", "api": "openai-compat", "api_key": "sk-raw"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := newTestRequest(http.MethodPost, "/v1/providers", c)
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(handler, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s, want 500", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-raw") {
		t.Fatal("the error echoed the key")
	}
}

// TestRecovererIgnoresClientAborts pins the difference between a bug and a
// disconnect: http.ErrAbortHandler is raised by net/http and ReverseProxy when
// the caller vanishes mid-response, and treating it as a panic turns every
// cancelled stream into an alarming log line.
func TestRecovererIgnoresClientAborts(t *testing.T) {
	// Reuse the real test server so the recoverer is exercised exactly as it is
	// wired in production.
	env := newTestEnv(t)
	srv := newServer(env.deps)
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	handler := srv.recoverer(panicking)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Body.Len() != 0 {
		t.Fatalf("a client abort produced a body: %q", rec.Body.String())
	}
}

// The file panel's "显示忽略文件" switch has to reach the server: the tree is
// filtered there, so a purely client-side flag can only hide more than the
// default and the switch looks broken (which is exactly what a user reported).
func TestFileTreeSwitchReachesTheServer(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, sess.ID)
	c := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodGet,
		"/v1/sessions/"+sess.ID+"/files/tree?path=&depth=1&include_ignored=1", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body.String())
	}
	env.workspace.mu.Lock()
	got := env.workspace.includeIgnored
	env.workspace.mu.Unlock()
	if !got {
		t.Fatal("include_ignored was accepted in the query but never reached the workspace")
	}

	// And the default stays filtered.
	if rec := env.doAuthed(http.MethodGet,
		"/v1/sessions/"+sess.ID+"/files/tree?path=&depth=1", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("tree: %d", rec.Code)
	}
	env.workspace.mu.Lock()
	got = env.workspace.includeIgnored
	env.workspace.mu.Unlock()
	if got {
		t.Fatal("the default tree request asked for ignored files")
	}
}
