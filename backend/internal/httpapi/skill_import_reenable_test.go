package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/provision"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// A disabled skill used to be replaced by an import without being re-enabled:
// the response said the import succeeded, the row stayed `enabled=0`, and a new
// session mounted nothing. The decision is that an import is an explicit intent
// to use the skill, so it re-enables it — and says so in the response.
//
// This test walks the whole promise: disable through the API, import the same
// name, read the response, then create a session against a real local sandbox
// and look at what the agent would actually see on disk.
func TestImportReenablesADisabledSkillAndSessionsMountIt(t *testing.T) {
	env := newTestEnvWithSkills(t)
	ctx := context.Background()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	// A real provisioner, so "mounted" means a file in the checkout rather than
	// a field in a fake.
	root := t.TempDir()
	local, err := sandbox.NewLocal(sandbox.LocalConfig{Root: root, InitialBranch: "main"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	env.deps.Provisioner = provision.New(provision.Config{
		Store:     env.store,
		Sandbox:   local,
		Git:       gitx.New(gitx.Config{}),
		SkillsDir: env.skillsDir,
	})
	env.rebuild()

	// 1. A skill with content, then disabled through the same endpoint the UI uses.
	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]string{
		"name":        "pdf-tools",
		"description": "Read and split PDFs.",
		"body":        "First version.\n",
	}, client)
	if created.Code != http.StatusCreated {
		t.Fatalf("create skill = %d %s", created.Code, created.Body.String())
	}
	var skillBody struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &skillBody)
	skillID := skillBody.Skill.ID

	disabled := env.doAuthed(http.MethodPatch, "/v1/skills/"+skillID, map[string]bool{"enabled": false}, client)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", disabled.Code, disabled.Body.String())
	}

	project, _ := env.mkSessionFor(user.ID, "import-target")

	// 2. A session created now must not mount the disabled skill.
	mountedBefore := createSessionAndMountPath(t, env, client, project.ID, root)
	if _, err := os.Stat(mountedBefore); !os.IsNotExist(err) {
		t.Fatalf("a disabled skill reached the checkout: %v", err)
	}

	// 3. Import a zip carrying the same name.
	archive := mkzip(t, []zipEntry{
		{name: "collection/pdf-tools/SKILL.md", body: "Second version.\n"},
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
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
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/import/zip", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range client.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-PF-CSRF", client.csrf)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	var imported struct {
		Skills []struct {
			Name      string `json:"name"`
			Skipped   bool   `json:"skipped"`
			Enabled   bool   `json:"enabled"`
			Reenabled bool   `json:"reenabled"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import: %v", err)
	}
	if len(imported.Skills) != 1 {
		t.Fatalf("import returned %d skills: %s", len(imported.Skills), rec.Body.String())
	}
	got := imported.Skills[0]
	if got.Skipped || !got.Enabled || !got.Reenabled {
		t.Fatalf("import result = %+v, want an enabled, re-enabled skill (the response is the promise)", got)
	}

	// The row agrees with the response.
	stored, err := env.store.Skill(ctx, user.ID, skillID)
	if err != nil {
		t.Fatalf("reload skill: %v", err)
	}
	if !stored.Enabled {
		t.Fatal("the response said enabled but the row is still disabled")
	}

	// 4. A session created after the import really has it, with the new content.
	mountedAfter := createSessionAndMountPath(t, env, client, project.ID, root)
	body2, err := os.ReadFile(filepath.Join(mountedAfter, "SKILL.md"))
	if err != nil {
		t.Fatalf("the re-enabled skill was not mounted: %v", err)
	}
	if !strings.Contains(string(body2), "Second version") {
		t.Fatalf("mounted SKILL.md = %q, want the imported content", body2)
	}

	// The list (what the UI renders) shows it enabled too.
	listed := env.doAuthed(http.MethodGet, "/v1/skills", nil, client)
	if listed.Code != http.StatusOK {
		t.Fatalf("list skills = %d", listed.Code)
	}
	var list struct {
		Skills []*contracts.Skill `json:"skills"`
	}
	env.mustJSON(listed, &list)
	for _, skill := range list.Skills {
		if skill.ID == skillID && !skill.Enabled {
			t.Fatal("the skill list still reports the imported skill as disabled")
		}
	}
}

// createSessionAndMountPath creates a main session through the API (which runs
// the real provisioner) and returns the directory the agent's SKILL.md would be
// copied to for the project's primary checkout.
func createSessionAndMountPath(
	t *testing.T,
	env *testEnv,
	client *creds,
	projectID, root string,
) string {
	t.Helper()
	rec := env.doAuthed(http.MethodPost, "/v1/projects/"+projectID+"/sessions", map[string]string{
		"title": "mounted",
	}, client)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &created)
	if created.Session.WorktreeID == "" {
		t.Fatalf("the session has no worktree: %s", rec.Body.String())
	}
	// The local provider's primary checkout: <root>/<user>/<project>/repo.
	return filepath.Join(root, created.Session.UserID, projectID, "repo", ".pi", "skills", "pdf-tools")
}
