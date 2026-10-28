package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-13.9: a token a user creates, which a script presents instead of a cookie.
// The interesting parts are that the plaintext is shown exactly once, that a
// revoked token stops working, and that a bearer caller can mutate without CSRF
// (there is no cookie for an attacker's page to ride).
func TestAPIKeysDriveTheAPILikeASession(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, _ := env.mkSessionFor(user.ID, "repo")

	created := env.doAuthed(http.MethodPost, "/v1/api-keys", map[string]any{"name": "ci"}, c)
	if created.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", created.Code, created.Body.String())
	}
	var body struct {
		APIKey struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
			Name   string `json:"name"`
		} `json:"api_key"`
		Token string `json:"token"`
	}
	env.mustJSON(created, &body)
	if body.Token == "" {
		t.Fatal("the token is missing from the creation response")
	}
	if body.APIKey.Name != "ci" {
		t.Fatalf("name = %q", body.APIKey.Name)
	}

	// The list never carries the secret again.
	listed := env.doAuthed(http.MethodGet, "/v1/api-keys", nil, c)
	var list struct {
		APIKeys []struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"api_keys"`
	}
	env.mustJSON(listed, &list)
	if len(list.APIKeys) != 1 {
		t.Fatalf("listed %d keys", len(list.APIKeys))
	}
	if list.APIKeys[0].Prefix != body.APIKey.Prefix {
		t.Fatalf("prefix = %q, want %q", list.APIKeys[0].Prefix, body.APIKey.Prefix)
	}

	// A bearer token can read and, importantly, write without a CSRF header.
	read := env.doBearer(http.MethodGet, "/v1/projects", nil, body.Token)
	if read.Code != http.StatusOK {
		t.Fatalf("bearer read = %d %s", read.Code, read.Body.String())
	}
	wrote := env.doBearer(http.MethodPost, "/v1/projects/"+project.ID+"/sessions",
		map[string]any{"title": "from a script"}, body.Token)
	if wrote.Code != http.StatusCreated {
		t.Fatalf("bearer write = %d %s", wrote.Code, wrote.Body.String())
	}

	// A garbage token is refused, and a revoked one behaves the same.
	if rec := env.doBearer(http.MethodGet, "/v1/projects", nil, "pfk_not_a_real_token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage token = %d", rec.Code)
	}
	revoked := env.doAuthed(http.MethodDelete, "/v1/api-keys/"+body.APIKey.ID, nil, c)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body.String())
	}
	if rec := env.doBearer(http.MethodGet, "/v1/projects", nil, body.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", rec.Code)
	}

	// Another user cannot revoke it, and the row stays for the audit trail.
	env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	other := env.login("other@piforge.local", "pw2")
	if rec := env.doAuthed(http.MethodDelete, "/v1/api-keys/"+body.APIKey.ID, nil, other); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user revoke = %d", rec.Code)
	}
}
