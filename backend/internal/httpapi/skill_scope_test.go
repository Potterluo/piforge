package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-14.1 / FR-16.3: a shared skill is an admin asset. Before this, only the
// delete half existed — an admin could remove a shared skill but had no way to
// create one, so the two-tier model was a promise the product could not keep.

func TestAdminCreatesASharedSkillAndEveryUserSeesIt(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	adminClient := env.login("admin@piforge.local", "pw")
	devClient := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"scope":       "admin",
		"name":        "team-conventions",
		"description": "团队约定",
		"body":        "# 约定\n\n先读团队规范。\n",
	}, adminClient)
	if created.Code != http.StatusCreated {
		t.Fatalf("admin create shared skill = %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &body)
	if body.Skill.Scope != contracts.SkillAdmin {
		t.Fatalf("scope = %q, want admin (an admin asked for a shared skill)", body.Skill.Scope)
	}
	sharedID := body.Skill.ID

	// Visible to a normal user, with its content intact and enabled.
	listed := env.doAuthed(http.MethodGet, "/v1/skills", nil, devClient)
	if listed.Code != http.StatusOK {
		t.Fatalf("dev list = %d %s", listed.Code, listed.Body.String())
	}
	var list struct {
		Skills []*contracts.Skill `json:"skills"`
	}
	env.mustJSON(listed, &list)
	var seen *contracts.Skill
	for _, skill := range list.Skills {
		if skill.ID == sharedID {
			seen = skill
		}
	}
	if seen == nil {
		t.Fatalf("the shared skill is not in the normal user's list: %s", listed.Body.String())
	}
	if seen.Scope != contracts.SkillAdmin || !seen.Enabled {
		t.Fatalf("shared skill = %+v, want enabled and scope=admin", seen)
	}

	bundle := env.doAuthed(http.MethodGet, "/v1/skills/"+sharedID+"/bundle", nil, devClient)
	if bundle.Code != http.StatusOK || !strings.Contains(bundle.Body.String(), "先读团队规范") {
		t.Fatalf("the normal user cannot read the shared content: %d %s", bundle.Code, bundle.Body.String())
	}

	// A second admin manages it too: shared means the deployment's, not the
	// creating admin's private property. (Toggling is role-gated, not owner-gated.)
	env.mkUser("admin2@piforge.local", "pw", contracts.RoleAdmin)
	admin2 := env.login("admin2@piforge.local", "pw")
	off := env.doAuthed(http.MethodPatch, "/v1/skills/"+sharedID, map[string]bool{"enabled": false}, admin2)
	if off.Code != http.StatusOK {
		t.Fatalf("a second admin could not switch the shared skill: %d %s", off.Code, off.Body.String())
	}
	on := env.doAuthed(http.MethodPatch, "/v1/skills/"+sharedID, map[string]bool{"enabled": true}, admin2)
	if on.Code != http.StatusOK {
		t.Fatalf("restore shared skill = %d %s", on.Code, on.Body.String())
	}
}

func TestNonAdminCannotCreateOrEditASharedSkill(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	adminClient := env.login("admin@piforge.local", "pw")
	devClient := env.login("dev@piforge.local", "pw")

	refused := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"scope": "admin",
		"name":  "not-yours",
		"body":  "# nope\n",
	}, devClient)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("a normal user created a shared skill: %d %s", refused.Code, refused.Body.String())
	}
	if !strings.Contains(refused.Body.String(), "管理员") {
		t.Fatalf("the refusal does not say who may create one: %s", refused.Body.String())
	}
	// A silently downgraded create would be worse than a refusal: it would look
	// like success in the UI while the skill never reached anyone else.
	var none struct {
		Skills []*contracts.Skill `json:"skills"`
	}
	list := env.doAuthed(http.MethodGet, "/v1/skills", nil, devClient)
	env.mustJSON(list, &none)
	for _, skill := range none.Skills {
		if skill.Name == "not-yours" {
			t.Fatalf("the refused create still produced a row: %+v", skill)
		}
	}

	// An admin makes one; the normal user may neither toggle nor delete it.
	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]any{
		"scope":       "admin",
		"name":        "shared-review",
		"description": "shared",
		"body":        "# shared review\n",
	}, adminClient)
	if created.Code != http.StatusCreated {
		t.Fatalf("admin create = %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &body)
	sharedID := body.Skill.ID

	patch := env.doAuthed(http.MethodPatch, "/v1/skills/"+sharedID, map[string]bool{"enabled": false}, devClient)
	if patch.Code != http.StatusForbidden {
		t.Fatalf("a normal user disabled a shared skill: %d %s", patch.Code, patch.Body.String())
	}
	del := env.doAuthed(http.MethodDelete, "/v1/skills/"+sharedID, nil, devClient)
	if del.Code != http.StatusForbidden {
		t.Fatalf("a normal user deleted a shared skill: %d %s", del.Code, del.Body.String())
	}
	// The bundle upload route is the other way to "edit" a shared skill.
	put := env.doAuthed(http.MethodPut, "/v1/skills/"+sharedID+"/bundle", map[string]any{
		"files": []map[string]string{{"path": "SKILL.md", "content": "# hijacked\n"}},
	}, devClient)
	if put.Code != http.StatusForbidden {
		t.Fatalf("a normal user replaced a shared bundle: %d %s", put.Code, put.Body.String())
	}

	// Import is an edit too: same name, different content must be skipped, and
	// the shared bundle must still be the admin's.
	archive := mkzip(t, []zipEntry{{name: "shared-review/SKILL.md", body: "# hijacked by import\n"}})
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "skills.zip")
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/import/zip", &upload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range devClient.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-PF-CSRF", devClient.csrf)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"skipped":true`) ||
		!strings.Contains(rec.Body.String(), "共享 Skill") {
		t.Fatalf("the import did not refuse the shared skill: %s", rec.Body.String())
	}
	bundle := env.doAuthed(http.MethodGet, "/v1/skills/"+sharedID+"/bundle", nil, devClient)
	if strings.Contains(bundle.Body.String(), "hijacked") {
		t.Fatalf("a normal user replaced a shared bundle through import: %s", bundle.Body.String())
	}
	if !strings.Contains(bundle.Body.String(), "shared review") {
		t.Fatalf("the shared bundle changed: %s", bundle.Body.String())
	}
}
