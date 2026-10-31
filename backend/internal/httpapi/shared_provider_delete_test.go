package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// Deleting a shared provider through the per-user route.
//
// The acceptance pass found the confusing half of the unshare story: the admin
// UI's 取消共享 button posts to /v1/admin/providers/{id}, while the per-user
// DELETE /v1/providers/{id} answers 404 not_found for the very same shared row
// (it is `source: inherited`, not a user row). The 404 reads as "this provider
// does not exist" and sends an operator looking for a row that is visible in the
// list, instead of saying "wrong endpoint for a shared provider".
//
// The fix has to keep two facts apart:
//
//   - a shared id exists but cannot be deleted here → 409 shared_provider, with
//     a message naming the endpoint that can;
//   - an id that is genuinely unknown (or somebody else's private row, which
//     must not be probeable) → 404 not_found, as before.
func TestDeletingASharedProviderThroughTheUserRouteExplainsItself(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")

	shared := env.doAuthed(http.MethodPost, "/v1/admin/providers", map[string]any{
		"name":     "shared-house",
		"api":      "openai-compat",
		"base_url": "https://house.example/v1",
		"api_key":  "sk-house-key",
		"models":   []map[string]string{{"id": "house-model"}},
	}, admin)
	if shared.Code != http.StatusOK {
		t.Fatalf("share: %d %s", shared.Code, shared.Body.String())
	}
	var created struct {
		Provider contracts.Provider `json:"provider"`
	}
	env.mustJSON(shared, &created)

	// A user who inherits the provider gets the typed, explanatory refusal, not
	// the "not found" that made the acceptance pass think it was gone.
	rec := env.doAuthed(http.MethodDelete, "/v1/providers/"+created.Provider.ID, nil, dev)
	env.mustErr(rec, http.StatusConflict, contracts.ErrSharedProvider)
	body := rec.Body.String()
	adminPath := "/v1/admin/providers/" + created.Provider.ID
	if !strings.Contains(body, adminPath) {
		t.Fatalf("the refusal does not name %s: %s", adminPath, body)
	}
	if !strings.Contains(body, "shared-house") {
		t.Fatalf("the refusal does not name the provider: %s", body)
	}
	if strings.Contains(body, contracts.ErrNotFound) {
		t.Fatalf("the refusal still says not_found: %s", body)
	}

	// The row is untouched — the wrong route must not have deleted it on the way
	// to reporting the error.
	listed := env.doAuthed(http.MethodGet, "/v1/admin/providers", nil, admin)
	if !strings.Contains(listed.Body.String(), "shared-house") {
		t.Fatalf("the shared provider disappeared: %s", listed.Body.String())
	}

	// The admin gets the same answer through the wrong route: the point is the
	// endpoint, not the role.
	rec = env.doAuthed(http.MethodDelete, "/v1/providers/"+created.Provider.ID, nil, admin)
	env.mustErr(rec, http.StatusConflict, contracts.ErrSharedProvider)

	// An id nobody created is still a 404 with the generic message: the typed
	// error must not become a way to enumerate ids.
	rec = env.doAuthed(http.MethodDelete, "/v1/providers/prv_does_not_exist", nil, dev)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
	if body := rec.Body.String(); strings.Contains(body, "/v1/admin/providers/") {
		t.Fatalf("an unknown id was explained as shared: %s", body)
	}

	// The caller's own provider still deletes normally; the shared refusal is
	// scoped to inherited rows.
	personal := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "personal", "api": "openai-compat",
		"base_url": "https://personal.example/v1", "models": []map[string]string{{"id": "p-model"}},
	}, dev)
	if personal.Code != http.StatusCreated {
		t.Fatalf("create personal: %d %s", personal.Code, personal.Body.String())
	}
	var mine struct {
		Provider contracts.Provider `json:"provider"`
	}
	env.mustJSON(personal, &mine)
	if rec := env.doAuthed(http.MethodDelete, "/v1/providers/"+mine.Provider.ID, nil, dev); rec.Code != http.StatusOK {
		t.Fatalf("deleting an own provider = %d %s", rec.Code, rec.Body.String())
	}

	// Somebody else's private provider still answers 404 rather than revealing
	// that the id exists and is merely not deletable.
	other := env.mkUser("other@piforge.local", "otherpw", contracts.RoleUser)
	theirs, err := env.store.CreateProvider(t.Context(), other.ID, store.ProviderInput{
		Name: "theirs", API: "openai-compat", BaseURL: "https://theirs.example/v1",
	})
	if err != nil {
		t.Fatalf("create other's provider: %v", err)
	}
	rec = env.doAuthed(http.MethodDelete, "/v1/providers/"+theirs.ID, nil, dev)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
}
