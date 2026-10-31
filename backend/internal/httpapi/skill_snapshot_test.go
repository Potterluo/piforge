package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/skillsnap"
	"github.com/piforge/piforge/backend/internal/store"
)

// seedSnapshot writes the per-session frozen skill tree FR-14.2 creates, so a
// delete test can assert the directory is gone rather than trusting the handler.
func seedSnapshot(t *testing.T, root, sessionID string) string {
	t.Helper()
	dir := skillsnap.Dir(root, sessionID)
	if err := os.MkdirAll(filepath.Join(dir, "code-review"), 0o755); err != nil {
		t.Fatalf("mkdir snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "code-review", "SKILL.md"), []byte("# Review\n"), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FR-14.2: deleting a session removes the snapshot directory that outlived it.
// Before this, every deleted session left one under <data>/skill-snapshots
// forever, and nothing ever looked at them again.
func TestDeleteSessionRemovesItsSnapshot(t *testing.T) {
	env := newTestEnv(t)
	dir := t.TempDir()
	env.deps.SnapshotDir = dir
	env.rebuild()

	user := env.mkUser("snap-session@piforge.local", "pw-snap-123", contracts.RoleUser)
	creds := env.login(user.Email, "pw-snap-123")
	_, session := env.mkSessionFor(user.ID, "snapshot-session")
	own := seedSnapshot(t, dir, session.ID)

	// Another session's snapshot is not this delete's business.
	other := "ses_01MOTHER00000000000000000"
	otherDir := seedSnapshot(t, dir, other)

	rec := env.do(http.MethodDelete, "/v1/sessions/"+session.ID, nil, creds, creds.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE session: %d %s", rec.Code, rec.Body.String())
	}
	if exists(own) {
		t.Fatalf("the deleted session's snapshot survived at %s", own)
	}
	if !exists(otherDir) {
		t.Fatalf("deleting one session removed another session's snapshot (%s)", otherDir)
	}
}

// FR-14.2: deleting a project removes every child session's snapshot. A branch
// session has its own checkout and therefore its own snapshot, so "one delete"
// has to reach more than the main session.
func TestDeleteProjectRemovesEveryChildSessionsSnapshot(t *testing.T) {
	env := newTestEnv(t)
	dir := t.TempDir()
	env.deps.SnapshotDir = dir
	env.rebuild()

	user := env.mkUser("snap-project@piforge.local", "pw-snap-123", contracts.RoleUser)
	creds := env.login(user.Email, "pw-snap-123")
	project, mainSession := env.mkSessionFor(user.ID, "snapshot-project")
	branch, err := env.store.CreateSession(context.Background(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Title: "branch", ModelRef: "shared/gpt",
	})
	if err != nil {
		t.Fatalf("CreateSession (branch): %v", err)
	}
	mainDir := seedSnapshot(t, dir, mainSession.ID)
	branchDir := seedSnapshot(t, dir, branch.ID)

	rec := env.do(http.MethodDelete, "/v1/projects/"+project.ID, nil, creds, creds.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE project: %d %s", rec.Code, rec.Body.String())
	}
	for _, gone := range []string{mainDir, branchDir} {
		if exists(gone) {
			t.Fatalf("a child session's snapshot survived the project delete: %s", gone)
		}
	}
}

// A refused project delete keeps the project, so it must keep the snapshots too:
// the user may retry once the holder is gone, and the sessions are still real.
func TestRefusedProjectDeleteKeepsTheSnapshots(t *testing.T) {
	env := newTestEnv(t)
	dir := t.TempDir()
	env.deps.SnapshotDir = dir
	env.rebuild()

	user := env.mkUser("snap-keep@piforge.local", "pw-snap-123", contracts.RoleUser)
	creds := env.login(user.Email, "pw-snap-123")
	project, session := env.mkSessionFor(user.ID, "snapshot-keep")
	snapshot := seedSnapshot(t, dir, session.ID)

	// A running session is a refusal the handler makes before it touches the
	// database, so the test marks the row running the way the runtime does.
	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(context.Background(), user.ID, session.ID,
		store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark session running: %v", err)
	}
	rec := env.do(http.MethodDelete, "/v1/projects/"+project.ID, nil, creds, creds.csrf)
	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE project with a running session: %d %s", rec.Code, rec.Body.String())
	}
	if !exists(snapshot) {
		t.Fatalf("a refused project delete removed the snapshot anyway: %s", snapshot)
	}
}
