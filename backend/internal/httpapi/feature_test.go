package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Feature flags exist to be enforced, not to hide buttons. The export endpoint is
// the sharpest case: "this tenant may not download transcripts" is a data
// governance decision, and a client-side hide is not a decision.
func TestFeatureFlagsAreEnforcedServerSide(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	owner := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	dev := env.login("dev@piforge.local", "devpw")
	_, sess := env.mkSessionFor(owner.ID, "repo")

	// Defaults are on.
	rec := env.doAuthed(http.MethodGet, "/v1/features", nil, dev)
	if rec.Code != http.StatusOK {
		t.Fatalf("features: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Features map[string]bool `json:"features"`
	}
	env.mustJSON(rec, &body)
	if !body.Features["export"] || !body.Features["browser"] {
		t.Fatalf("defaults should be on: %+v", body.Features)
	}
	if export := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/export", nil, dev); export.Code != http.StatusOK {
		t.Fatalf("export while enabled = %d", export.Code)
	}

	// The admin turns export off for that user.
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/users/"+owner.ID+"/features",
		map[string]any{"flag": "export", "enabled": false}, admin); rec.Code != http.StatusOK {
		t.Fatalf("set feature: %d %s", rec.Code, rec.Body.String())
	}
	if export := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/export", nil, dev); export.Code != http.StatusForbidden {
		t.Fatalf("export after disabling = %d (%s)", export.Code, export.Body.String())
	}

	// The user sees the flag, and cannot change it.
	rec = env.doAuthed(http.MethodGet, "/v1/features", nil, dev)
	env.mustJSON(rec, &body)
	if body.Features["export"] {
		t.Fatalf("the client still thinks export is on: %+v", body.Features)
	}
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/users/"+owner.ID+"/features",
		map[string]any{"flag": "export", "enabled": true}, dev); rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user flipped their own flag: %d", rec.Code)
	}

	// An unknown flag is a client error, not a silent no-op.
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/users/"+owner.ID+"/features",
		map[string]any{"flag": "nonsense", "enabled": true}, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown flag accepted: %d", rec.Code)
	}
}
