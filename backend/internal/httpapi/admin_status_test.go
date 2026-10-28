package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The status screen is admin-only and counts real rows: an operator opens it when
// something already feels wrong, so it has to be right rather than pretty.
func TestAdminStatusCountsAndIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	adminUser := env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")
	env.mkSessionFor(adminUser.ID, "repo")

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Users       int `json:"users"`
		UsersActive int `json:"users_active"`
		Sessions    int `json:"sessions"`
		Projects    int `json:"projects"`
		Providers   int `json:"providers"`
	}
	env.mustJSON(rec, &body)
	if body.Users < 2 || body.Sessions < 1 || body.Projects < 1 {
		t.Fatalf("counts look empty: %+v", body)
	}
	if body.UsersActive != body.Users {
		t.Fatalf("every user in this fixture is active: %+v", body)
	}

	if rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, dev); rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user read the status page: %d", rec.Code)
	}
}
