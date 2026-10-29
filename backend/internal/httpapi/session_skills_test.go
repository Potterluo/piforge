package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// TestSessionSkillsEndpoint covers FR-14.5's read surface: the session reports
// the versions it was mounted with, an empty list is an array (not null), and
// another user's session is a 404 like every other session route.
func TestSessionSkillsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	owner := env.mkUser("owner@piforge.local", "pw", contracts.RoleUser)
	other := env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	_ = env.mkUser("admin@piforge.local", "admin", contracts.RoleAdmin)
	c := env.login("owner@piforge.local", "pw")

	_, sess := env.mkSessionFor(owner.ID, "versioned")
	ctx := context.Background()

	// Before provisioning: an empty list, so the UI has one shape to render.
	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/skills", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET session skills = %d body %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Skills []*contracts.SessionSkill `json:"skills"`
	}
	env.mustJSON(rec, &empty)
	if empty.Skills == nil {
		t.Fatal("skills is null, want []")
	}
	if len(empty.Skills) != 0 {
		t.Fatalf("skills = %v, want none", empty.Skills)
	}

	if _, err := env.store.RecordSessionSkills(ctx, owner.ID, sess.ID, []store.NewSessionSkill{{
		SkillID:     "skl_review",
		Name:        "code-review",
		Version:     "1.0.0",
		ContentHash: "abcdef0123456789",
		Description: "review the diff",
		MountedPath: "/data/sandboxes/repo/.pi/skills/code-review",
	}}); err != nil {
		t.Fatalf("RecordSessionSkills: %v", err)
	}

	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/skills", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET session skills = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Skills []*contracts.SessionSkill `json:"skills"`
	}
	env.mustJSON(rec, &body)
	if len(body.Skills) != 1 {
		t.Fatalf("skills = %+v, want one record", body.Skills)
	}
	got := body.Skills[0]
	if got.SessionID != sess.ID || got.SkillID != "skl_review" || got.Name != "code-review" {
		t.Fatalf("identity fields = %+v", got)
	}
	if got.Version != "1.0.0" || got.ContentHash != "abcdef0123456789" {
		t.Fatalf("version fields = %+v", got)
	}
	if got.MountedPath != "/data/sandboxes/repo/.pi/skills/code-review" || got.RecordedAt.IsZero() {
		t.Fatalf("provenance fields = %+v", got)
	}

	// Another user sees "not found", never the record.
	otherCreds := env.login("other@piforge.local", "pw2")
	_ = other
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/skills", nil, otherCreds)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)

	// Unauthenticated is a 401, not an empty list.
	rec = env.do(http.MethodGet, "/v1/sessions/"+sess.ID+"/skills", nil, nil, "")
	env.mustErr(rec, http.StatusUnauthorized, contracts.ErrUnauthenticated)
}
