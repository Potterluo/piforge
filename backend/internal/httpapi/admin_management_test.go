package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The admin area is where a self-hosted deployment onboards its tenants: there is
// no public signup, so "create a user" and "make somebody an admin" have to work
// through the API, and both have to refuse the moves that lock the deployment out
// of its own admin area.

func TestAdminCanCreateAUserAndPromoteThem(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")

	rec := env.doAuthed(http.MethodPost, "/v1/admin/users", map[string]any{
		"email":    "new@example.com",
		"password": "longenough",
		"role":     "user",
	}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d body = %s", rec.Code, rec.Body.String())
	}

	// The account works: it can log in.
	created := env.login("new@example.com", "longenough")
	if created == nil {
		t.Fatal("the created account cannot log in")
	}

	// Promote it.
	user, _, err := env.store.UserByEmail(t.Context(), "new@example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rec = env.doAuthed(http.MethodPatch, "/v1/admin/users/"+user.ID, map[string]any{
		"role": "admin",
	}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: status = %d body = %s", rec.Code, rec.Body.String())
	}
	after, err := env.store.UserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Role != contracts.RoleAdmin {
		t.Fatalf("role = %q", after.Role)
	}
}

func TestTheLastAdminCannotBeDemoted(t *testing.T) {
	env := newTestEnv(t)
	admin := env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	creds := env.login("admin@piforge.local", "adminpw")

	rec := env.doAuthed(http.MethodPatch, "/v1/admin/users/"+admin.ID, map[string]any{
		"role": "user",
	}, creds)
	// The store reports ErrConflict, which the API maps to 409.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	after, err := env.store.UserByID(t.Context(), admin.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Role != contracts.RoleAdmin {
		t.Fatal("the last admin was demoted")
	}
}

func TestAdminCannotDisableThemselves(t *testing.T) {
	env := newTestEnv(t)
	admin := env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	creds := env.login("admin@piforge.local", "adminpw")

	rec := env.doAuthed(http.MethodPatch, "/v1/admin/users/"+admin.ID, map[string]any{
		"status": "disabled",
	}, creds)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestOnlyAdminsMayManageUsers(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")

	rec := env.doAuthed(http.MethodPost, "/v1/admin/users", map[string]any{
		"email":    "x@example.com",
		"password": "longenough",
	}, dev)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user created an account: status = %d", rec.Code)
	}
}

// Model management is the provider patch: a model id typed in the UI has to be
// editable afterwards, and rotating a key must not require recreating the
// provider (the user pasted it once).
func TestPatchProviderEditsModelsAndKeepsTheKey(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	creds := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name":     "mine",
		"api":      "openai-compat",
		"base_url": "https://api.example.com/v1",
		"api_key":  "sk-first",
		"models":   []map[string]string{{"id": "custom-model-1"}},
	}, creds)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Provider contracts.Provider `json:"provider"`
	}
	env.mustJSON(rec, &created)

	// Edit the model list without touching the key.
	rec = env.doAuthed(http.MethodPatch, "/v1/providers/"+created.Provider.ID, map[string]any{
		"models": []map[string]string{{"id": "custom-model-1"}, {"id": "custom-model-2"}},
	}, creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	var patched struct {
		Provider contracts.Provider `json:"provider"`
	}
	env.mustJSON(rec, &patched)
	if len(patched.Provider.Models) != 2 {
		t.Fatalf("models = %+v", patched.Provider.Models)
	}
	if !patched.Provider.HasKey {
		t.Fatal("patching the model list dropped the key")
	}

	// A provider belonging to somebody else is not the caller's to patch.
	other := env.mkUser("other@example.com", "pw", contracts.RoleUser)
	if _, err := env.store.CreateProvider(t.Context(), other.ID, store.ProviderInput{
		Name: "theirs", API: "openai-compat", BaseURL: "https://theirs.example.com/v1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	theirs, err := env.store.ListProviders(t.Context(), other.ID)
	if err != nil || len(theirs) == 0 {
		t.Fatalf("seed: %v", err)
	}
	rec = env.doAuthed(http.MethodPatch, "/v1/providers/"+theirs[0].ID, map[string]any{
		"base_url": "https://evil.example.com",
	}, creds)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Fatalf("patched someone else's provider: %d %s", rec.Code, rec.Body.String())
	}
	_ = user
}

// The new-session dialog promises that an empty title is filled in from the first
// message. A placeholder title makes a sidebar of identical rows.
func TestFirstMessageNamesAnUntitledSession(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	// Clear the title, the way the dialog does when the field is left empty.
	if _, err := env.store.UpdateSession(t.Context(), user.ID, sess.ID,
		store.SessionPatch{Title: ptrString("")}); err != nil {
		t.Fatalf("clear title: %v", err)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "  请帮我看看这个仓库的目录结构  "}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}

	reloaded, err := env.store.Session(t.Context(), user.ID, sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Title != "请帮我看看这个仓库的目录结构" {
		t.Fatalf("title = %q", reloaded.Title)
	}
}

// A message that names a provider nobody configured is the user's to fix, and the
// error has to say where. "internal error" sent people looking at the wrong thing.
func TestSendWithoutAModelSaysWhereToFixIt(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	p, err := env.store.CreateProject(t.Context(), user.ID, store.NewProject{Name: "p", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	sess, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: p.ID, Kind: contracts.KindMain, Title: "x", ModelRef: "ghost/model",
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// The runtime fails because the model cannot resolve; the response must name
	// the configuration problem instead of passing "internal error" through.
	env.runtime.promptErr = contracts.NewError(contracts.ErrInternal, "spawn failed")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)

	// With a resolvable provider the same runtime failure is reported as itself:
	// configuration is only blamed when it is actually the cause.
	env.ensureProvider(user.ID, "ghost", "model")
	env.runtime.promptErr = contracts.NewError(contracts.ErrInternal, "spawn failed")
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_2", "text": "hi"}, c)
	env.mustErr(rec, http.StatusInternalServerError, contracts.ErrInternal)
}

func ptrString(v string) *string { return &v }
