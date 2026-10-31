package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/runtimedirs"
	"github.com/piforge/piforge/backend/internal/store"
)

// seedSessionDir writes one file under <root>/<session>, the shape the product
// creates for pi-agents and artifacts.
func seedSessionDir(t *testing.T, root, sessionID, file string) string {
	t.Helper()
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(filepath.Join(dir, "screenshots"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "screenshots", file), []byte("png"), 0o644); err != nil {
		t.Fatalf("write %s: %v", dir, err)
	}
	return dir
}

// Deleting a session reclaims every directory keyed by its id, not only the
// frozen skill snapshot. The agent directory and the screenshots used to be
// written and never removed: 17 pi-agents directories were already on the
// deployed volume when this test was written.
func TestDeleteSessionReclaimsEveryDirectoryItOwns(t *testing.T) {
	env := newTestEnv(t)
	root := t.TempDir()
	env.deps.SnapshotDir = filepath.Join(root, "skill-snapshots")
	env.deps.AgentDir = filepath.Join(root, "pi-agents")
	env.deps.ArtifactsDir = filepath.Join(root, "artifacts")
	env.rebuild()

	user := env.mkUser("dirs-session@piforge.local", "pw-dirs-123", contracts.RoleUser)
	creds := env.login(user.Email, "pw-dirs-123")
	_, session := env.mkSessionFor(user.ID, "dirs-session")

	owned := []string{
		seedSnapshot(t, env.deps.SnapshotDir, session.ID),
		seedSessionDir(t, env.deps.AgentDir, session.ID, "noop"),
		seedSessionDir(t, env.deps.ArtifactsDir, session.ID, "one.png"),
	}
	// Another session's directories are not this delete's business.
	other := "ses_01MOTHER00000000000000000"
	otherDirs := []string{
		seedSnapshot(t, env.deps.SnapshotDir, other),
		seedSessionDir(t, env.deps.AgentDir, other, "noop"),
		seedSessionDir(t, env.deps.ArtifactsDir, other, "one.png"),
	}

	rec := env.do(http.MethodDelete, "/v1/sessions/"+session.ID, nil, creds, creds.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE session: %d %s", rec.Code, rec.Body.String())
	}
	for _, dir := range owned {
		if exists(dir) {
			t.Fatalf("the deleted session's directory survived at %s", dir)
		}
	}
	for _, dir := range otherDirs {
		if !exists(dir) {
			t.Fatalf("deleting one session removed another session's directory (%s)", dir)
		}
	}
}

// Deleting a project reclaims the same three directories for every child
// session (branch included), plus the idle reaper's manifest directory for the
// sandboxes the project owned.
func TestDeleteProjectReclaimsChildDirectoriesAndSandboxSnapshots(t *testing.T) {
	env := newTestEnv(t)
	root := t.TempDir()
	env.deps.SnapshotDir = filepath.Join(root, "skill-snapshots")
	env.deps.AgentDir = filepath.Join(root, "pi-agents")
	env.deps.ArtifactsDir = filepath.Join(root, "artifacts")
	env.deps.SandboxSnapshotDir = filepath.Join(root, "snapshots")
	env.rebuild()

	user := env.mkUser("dirs-project@piforge.local", "pw-dirs-123", contracts.RoleUser)
	creds := env.login(user.Email, "pw-dirs-123")
	project, mainSession := env.mkSessionFor(user.ID, "dirs-project")
	branch, err := env.store.CreateSession(context.Background(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Title: "branch", ModelRef: "shared/gpt",
	})
	if err != nil {
		t.Fatalf("CreateSession (branch): %v", err)
	}
	sb, err := env.store.CreateSandbox(context.Background(), user.ID, store.NewSandbox{
		ID:        "sbx_01MDIRS00000000000000000A",
		ProjectID: project.ID,
		State:     contracts.SandboxReady,
		Provider:  "local",
		RootDir:   filepath.Join(root, "sandboxes", "usr_x", project.ID),
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	owned := []string{
		seedSnapshot(t, env.deps.SnapshotDir, mainSession.ID),
		seedSnapshot(t, env.deps.SnapshotDir, branch.ID),
		seedSessionDir(t, env.deps.AgentDir, mainSession.ID, "noop"),
		seedSessionDir(t, env.deps.AgentDir, branch.ID, "noop"),
		seedSessionDir(t, env.deps.ArtifactsDir, mainSession.ID, "one.png"),
		seedSessionDir(t, env.deps.ArtifactsDir, branch.ID, "one.png"),
		seedSessionDir(t, env.deps.SandboxSnapshotDir, sb.ID, "manifest.json"),
	}

	rec := env.do(http.MethodDelete, "/v1/projects/"+project.ID, nil, creds, creds.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE project: %d %s", rec.Code, rec.Body.String())
	}
	for _, dir := range owned {
		if exists(dir) {
			t.Fatalf("a child directory survived the project delete: %s", dir)
		}
	}
}

// The admin status block reports the same inventory the janitor sweeps, with
// entry counts and bytes, so growth is visible without shelling into the
// container. The endpoint stays admin-only (covered in admin_status_test.go).
func TestAdminStatusReportsPerDirectoryUsage(t *testing.T) {
	env := newTestEnv(t)
	env.deps.DataDir = t.TempDir()
	env.rebuild()

	if err := os.MkdirAll(filepath.Join(env.deps.DataDir, "artifacts", "ses_ONE", "screenshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.deps.DataDir, "artifacts", "ses_ONE", "screenshots", "a.png"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.deps.DataDir, "pi-agents", "ses_ONE"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.deps.DataDir, "pi-agents", "ses_ONE", "models.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	adminUser := env.mkUser("dirs-admin@piforge.local", "adminpw", contracts.RoleAdmin)
	_ = adminUser
	admin := env.login("dirs-admin@piforge.local", "adminpw")

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Storage []runtimedirs.Usage `json:"storage"`
		Bytes   int64               `json:"storage_total_bytes"`
	}
	env.mustJSON(rec, &body)
	if len(body.Storage) != len(runtimedirs.Dirs()) {
		t.Fatalf("storage has %d rows, want one per inventory directory (%d): %+v",
			len(body.Storage), len(runtimedirs.Dirs()), body.Storage)
	}
	byName := map[string]runtimedirs.Usage{}
	for _, u := range body.Storage {
		byName[u.Name] = u
	}
	artifacts := byName["artifacts"]
	if artifacts.Entries != 1 || artifacts.Bytes != 5 {
		t.Fatalf("artifacts usage = %+v, want 1 entry and 5 bytes", artifacts)
	}
	agents := byName["pi-agents"]
	if agents.Entries != 1 || agents.Bytes != 2 {
		t.Fatalf("pi-agents usage = %+v, want 1 entry and 2 bytes", agents)
	}
	// Every directory in the inventory is represented, even when missing.
	for _, entry := range runtimedirs.Dirs() {
		if _, ok := byName[entry.Name]; !ok {
			t.Fatalf("the status block does not report %q", entry.Name)
		}
	}
	if body.Bytes != 7 {
		t.Fatalf("storage_total_bytes = %d, want 7", body.Bytes)
	}
}

// A deployment that never wrote a runtime directory still reports the full
// inventory (zeroes), rather than omitting the row and hiding the leak class.
func TestAdminStatusReportsEmptyDirectoriesAtZero(t *testing.T) {
	env := newTestEnv(t)
	env.deps.DataDir = t.TempDir()
	env.rebuild()
	env.mkUser("dirs-empty@piforge.local", "adminpw", contracts.RoleAdmin)
	creds := env.login("dirs-empty@piforge.local", "adminpw")

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Storage []runtimedirs.Usage `json:"storage"`
	}
	env.mustJSON(rec, &body)
	if len(body.Storage) != len(runtimedirs.Dirs()) {
		t.Fatalf("storage = %d rows, want %d", len(body.Storage), len(runtimedirs.Dirs()))
	}
	for _, u := range body.Storage {
		if u.Entries != 0 || u.Bytes != 0 {
			t.Fatalf("a never-created directory reported usage: %+v", u)
		}
	}
	// JSON must be an array, not null: the page maps over it.
	raw, _ := json.Marshal(body.Storage)
	if string(raw) == "null" {
		t.Fatal("storage serialised as null")
	}
}
