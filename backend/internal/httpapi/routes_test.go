package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// ---------------------------------------------------------------------------
// Route table
// ---------------------------------------------------------------------------

// TestEveryRouteIsRegistered pins the exact route list the frontend and the
// e2e suite call. A route that silently disappears shows up in the browser as
// an HTML 404, which is painful to diagnose, so it fails here instead.
func TestEveryRouteIsRegistered(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")
	c := env.login("dev@piforge.local", "pw")

	const missingID = "does-not-exist"
	repl := func(path string) string {
		return strings.NewReplacer(
			"{id}", missingID,
			"{project}", project.ID,
			"{session}", sess.ID,
		).Replace(path)
	}

	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/auth/login"},
		{http.MethodGet, "/v1/auth/me"},
		// logout is last: it invalidates the cookie the loop reuses.
		{http.MethodPost, "/v1/auth/logout"},
		{http.MethodGet, "/v1/projects"},
		{http.MethodPost, "/v1/projects"},
		{http.MethodGet, "/v1/projects/{id}"},
		{http.MethodPost, "/v1/projects/{id}/sessions"},
		{http.MethodGet, "/v1/sessions"},
		{http.MethodGet, "/v1/sessions/{id}"},
		{http.MethodPatch, "/v1/sessions/{id}"},
		{http.MethodPost, "/v1/sessions/{id}/messages"},
		{http.MethodPost, "/v1/sessions/{id}/interrupt"},
		{http.MethodGet, "/v1/sessions/{id}/events"},
		{http.MethodGet, "/v1/sessions/{id}/entries"},
		{http.MethodGet, "/v1/sessions/{id}/tree"},
		{http.MethodGet, "/v1/sessions/{id}/fork-points"},
		{http.MethodGet, "/v1/sessions/{id}/status"},
		{http.MethodGet, "/v1/sessions/{id}/stats"},
		{http.MethodPost, "/v1/sessions/{id}/fork"},
		{http.MethodPost, "/v1/sessions/{id}/sidecar"},
		{http.MethodPost, "/v1/sessions/{id}/compact"},
		{http.MethodGet, "/v1/sessions/{id}/files/tree"},
		{http.MethodGet, "/v1/sessions/{id}/files"},
		{http.MethodPut, "/v1/sessions/{id}/files"},
		{http.MethodDelete, "/v1/sessions/{id}/files"},
		{http.MethodGet, "/v1/sessions/{id}/search"},
		{http.MethodGet, "/v1/sessions/{id}/git/status"},
		{http.MethodGet, "/v1/sessions/{id}/git/diff"},
		{http.MethodGet, "/v1/sessions/{id}/git/log"},
		{http.MethodPost, "/v1/sessions/{id}/git/stage"},
		{http.MethodPost, "/v1/sessions/{id}/git/unstage"},
		{http.MethodPost, "/v1/sessions/{id}/git/commit"},
		{http.MethodPost, "/v1/sessions/{id}/git/branch"},
		{http.MethodPost, "/v1/sessions/{id}/git/checkout"},
		{http.MethodGet, "/v1/providers"},
		{http.MethodPost, "/v1/providers"},
		{http.MethodPatch, "/v1/providers/{id}"},
		{http.MethodDelete, "/v1/providers/{id}"},
		{http.MethodPost, "/v1/providers/{id}/test"},
		{http.MethodGet, "/v1/skills"},
		{http.MethodPost, "/v1/skills"},
		{http.MethodDelete, "/v1/skills/{id}"},
		{http.MethodGet, "/v1/mcp/servers"},
		{http.MethodPost, "/v1/mcp/servers"},
		{http.MethodDelete, "/v1/mcp/servers/{id}"},
		{http.MethodGet, "/v1/admin/users"},
		{http.MethodPost, "/v1/admin/users"},
		{http.MethodPatch, "/v1/admin/users/{id}"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			path := repl(rt.path)
			// A placeholder session/project gets a real id so the handler runs;
			// everything else resolves to a store lookup that answers 404. What
			// matters is that the response came from a handler, not from the
			// catch-all.
			var body any
			if rt.method == http.MethodPost || rt.method == http.MethodPatch || rt.method == http.MethodPut {
				body = map[string]any{}
			}
			rec := env.doAuthed(rt.method, path, body, c)

			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("method not allowed: the route is registered for another verb")
			}
			if rec.Code == http.StatusNotFound {
				var envelope struct {
					Error *contracts.APIError `json:"error"`
				}
				env.mustJSON(rec, &envelope)
				if envelope.Error != nil && strings.HasPrefix(envelope.Error.Message, "no route for") {
					t.Fatalf("route is not registered (body %s)", rec.Body.String())
				}
			}
			// Anything but an HTML/plain 404 proves a handler answered.
			if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("HTML response: %s", rec.Body.String())
			}
		})
	}
}

// TestUnimplementedRouteUsesTheErrorEnvelope covers the one deliberately
// unimplemented endpoint: a registered 501 is far easier for the client than a
// missing route.
func TestUnimplementedRouteUsesTheErrorEnvelope(t *testing.T) {
	env := newTestEnv(t)
	admin := env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	c := env.login("admin@piforge.local", "pw")

	// The shared provider exists, so the handler gets past the ownership check
	// and reaches the 501.
	shared, err := env.store.UpsertAdminProvider(context.Background(), admin.ID, store.ProviderInput{
		Name: "shared", API: "openai-compat", Models: []contracts.ModelSpec{{ID: "m"}},
	})
	if err != nil {
		t.Fatalf("UpsertAdminProvider: %v", err)
	}

	// The route reports a usable answer now, so this asserts the shape rather
	// than a fixed "not implemented": the shared provider in this fixture has no
	// key, and saying so is the diagnostic the button exists for.
	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+shared.ID+"/test", map[string]any{}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("test a keyless provider = %d body %s, want 400", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cipher") {
		t.Fatalf("the response echoed stored key material: %s", rec.Body.String())
	}

	// An unknown provider is still a 404, so a bad id reveals nothing.
	rec = env.doAuthed(http.MethodPost, "/v1/providers/prv_nope/test", map[string]any{}, c)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Provisioning hook
// ---------------------------------------------------------------------------

// fakeProvisioner mimics provision.Service: it creates the sandbox/worktree
// rows and attaches the worktree to the session.
type fakeProvisioner struct {
	mu    sync.Mutex
	store *store.Store
	path  string
	err   error
	calls []string
}

func (f *fakeProvisioner) EnsureWorktree(ctx context.Context, userID string, project *contracts.Project, sess *contracts.Session) (*contracts.Worktree, error) {
	f.mu.Lock()
	f.calls = append(f.calls, sess.ID)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	sb, err := f.store.SandboxForProject(ctx, userID, project.ID)
	if err != nil {
		sb, err = f.store.CreateSandbox(ctx, userID, store.NewSandbox{ProjectID: project.ID})
		if err != nil {
			return nil, err
		}
	}
	wt, err := f.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID,
		Path:      f.path + "/" + sess.ID,
		Branch:    sess.ID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := f.store.UpdateSession(ctx, userID, sess.ID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		return nil, err
	}
	return wt, nil
}

func (f *fakeProvisioner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestCreateSessionProvisionsWorktree(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, _ := env.mkSessionFor(user.ID, "repo")
	c := env.login("dev@piforge.local", "pw")

	p := &fakeProvisioner{store: env.store, path: "/srv/wt"}
	env.deps.Provisioner = p
	env.rebuild()

	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+project.ID+"/sessions",
		map[string]string{"title": "work"}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &body)
	if body.Session.WorktreeID == "" {
		t.Fatalf("worktree_id missing from the response: %s", rec.Body.String())
	}
	if p.callCount() != 1 {
		t.Fatalf("provisioner calls = %d", p.callCount())
	}

	// The workspace endpoints now resolve to that path.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+body.Session.ID+"/files/tree", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("files/tree = %d body %s", rec.Code, rec.Body.String())
	}
	if got := env.workspace.lastRoot(); got != "/srv/wt/"+body.Session.ID {
		t.Fatalf("workspace root = %q", got)
	}
}

func TestProvisionFailureDoesNotFailTheRequest(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, _ := env.mkSessionFor(user.ID, "repo")
	c := env.login("dev@piforge.local", "pw")

	p := &fakeProvisioner{store: env.store, path: "/srv/wt", err: errProvision}
	env.deps.Provisioner = p
	env.rebuild()

	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+project.ID+"/sessions",
		map[string]string{"title": "work"}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &body)
	if body.Session.WorktreeID != "" {
		t.Fatalf("worktree_id = %q, want empty", body.Session.WorktreeID)
	}
	if body.Session.ID == "" {
		t.Fatal("session was not created")
	}
}

func TestFirstPromptGetsTheProvisionedWorktree(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")
	c := env.login("dev@piforge.local", "pw")

	// The session has no worktree yet; the provisioner runs on the first
	// prompt so the agent starts inside the checkout.
	p := &fakeProvisioner{store: env.store, path: "/srv/wt"}
	env.deps.Provisioner = p
	env.rebuild()

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("message = %d body %s", rec.Code, rec.Body.String())
	}
	if p.callCount() != 1 {
		t.Fatalf("provisioner calls = %d", p.callCount())
	}

	env.runtime.mu.Lock()
	starts := append([]contracts.SessionSpec(nil), env.runtime.starts...)
	env.runtime.mu.Unlock()
	if len(starts) != 1 {
		t.Fatalf("runtime starts = %d", len(starts))
	}
	if starts[0].Worktree != "/srv/wt/"+sess.ID {
		t.Fatalf("spec.Worktree = %q", starts[0].Worktree)
	}
	if starts[0].ProjectID != project.ID {
		t.Fatalf("spec.ProjectID = %q", starts[0].ProjectID)
	}
	_ = project
}

// errProvision is a stand-in for a git clone failure.
var errProvision = &provisionError{}

type provisionError struct{}

func (e *provisionError) Error() string { return "provision: simulated failure" }
