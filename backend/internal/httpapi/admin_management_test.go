package httpapi

import (
	"net/http"
	"strings"
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

// A shared provider is how a deployment gives everybody a model without handing
// out keys: the admin configures one, every user inherits it, and a user's own
// provider of the same name shadows it (PRD FR-16.2). Until this existed the
// policy was implemented in the store and unreachable from the product.
func TestAdminSharesAProviderThatUsersInherit(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")

	rec := env.doAuthed(http.MethodPost, "/v1/admin/providers", map[string]any{
		"name":     "shared-house",
		"api":      "openai-compat",
		"base_url": "https://shared.example.com/v1",
		"api_key":  "sk-house-key",
		"models":   []map[string]string{{"id": "house-model"}},
	}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}

	// The user sees it as inherited, not as something they can edit.
	listed := env.doAuthed(http.MethodGet, "/v1/providers", nil, dev)
	var body struct {
		Providers []contracts.Provider `json:"providers"`
	}
	env.mustJSON(listed, &body)
	var found *contracts.Provider
	for i := range body.Providers {
		if body.Providers[i].Name == "shared-house" {
			found = &body.Providers[i]
		}
	}
	if found == nil {
		t.Fatalf("the shared provider is not visible to a normal user: %+v", body.Providers)
	}
	if found.Source != contracts.SourceInherited {
		t.Fatalf("source = %q", found.Source)
	}

	// The key never comes back out, to either of them.
	if listed := env.doAuthed(http.MethodGet, "/v1/admin/providers", nil, admin); strings.Contains(listed.Body.String(), "sk-house-key") {
		t.Fatal("the shared key was echoed")
	}

	// A normal user cannot create, list or delete shared providers.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/providers"},
		{http.MethodPost, "/v1/admin/providers"},
	} {
		if rec := env.doAuthed(tc.method, tc.path, map[string]any{"name": "x", "api": "openai-compat"}, dev); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s by a normal user = %d", tc.method, tc.path, rec.Code)
		}
	}
}

// The inherited default provider on a fresh deployment exists but has no key.
// That is the most common way a first admin meets this error, and "internal
// error" was the previous answer.
func TestSendWithAKeylessProviderSaysItHasNoKey(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	c := env.login("admin@piforge.local", "pw")
	p, err := env.store.CreateProject(t.Context(), user.ID, store.NewProject{Name: "p", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := env.store.UpsertAdminProvider(t.Context(), user.ID, store.ProviderInput{
		Name:    "shared-house",
		API:     "openai-compat",
		BaseURL: "https://house.example.invalid/v1",
		Models:  []contracts.ModelSpec{{ID: "house-model"}},
	}); err != nil {
		t.Fatalf("provider: %v", err)
	}
	sess, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: p.ID, Kind: contracts.KindMain, Title: "x", ModelRef: "shared-house/house-model",
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	env.runtime.promptErr = contracts.NewError(contracts.ErrInternal, "spawn failed")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)
	if !strings.Contains(rec.Body.String(), "no API key") {
		t.Fatalf("the message does not mention the missing key: %s", rec.Body.String())
	}
}

// The spawn path is where a missing model actually fails: with no provider the
// child has no model to load and exits before reading a prompt. That error used
// to escape as "internal error" even after the prompt path learned to explain it.
func TestSpawnFailureWithNoProviderIsExplained(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("solo@piforge.local", "pw", contracts.RoleAdmin)
	c := env.login("solo@piforge.local", "pw")
	p, err := env.store.CreateProject(t.Context(), user.ID, store.NewProject{Name: "p", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	sess, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: p.ID, Kind: contracts.KindMain, Title: "x", ModelRef: "shared-anthropic/claude",
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// The deployment's default provider is what a session with no model_ref uses.
	// It exists on a fresh install and has no key, which is the case that used to
	// answer "internal error".
	if _, err := env.store.UpsertAdminProvider(t.Context(), user.ID, store.ProviderInput{
		Name: "shared-anthropic", API: "anthropic-messages",
		Models: []contracts.ModelSpec{{ID: "claude-sonnet-4"}},
	}); err != nil {
		t.Fatalf("default provider: %v", err)
	}
	env.deps.DefaultProvider = "shared-anthropic"
	env.rebuild()
	if _, err := env.store.UpdateSession(t.Context(), user.ID, sess.ID,
		store.SessionPatch{ModelRef: ptrString("")}); err != nil {
		t.Fatalf("clear model: %v", err)
	}
	if _, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: p.ID, Kind: contracts.KindMain, Title: "y",
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	env.runtime.startErr = contracts.NewError(contracts.ErrInternal, "spawn failed")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "hi"}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)
	if !strings.Contains(rec.Body.String(), "Settings") {
		t.Fatalf("the message does not say where to fix it: %s", rec.Body.String())
	}
}
