package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// TestDeleteProjectReconcilesAStaleRunningSession is the user-visible half of
// the restart defect: a session says "running" in SQLite, no agent process
// exists behind it, and the delete guard used to believe the row forever.
//
// The row and the run are what a process killed mid-turn leaves (the hub writes
// "running" on run.started and "idle" only when a settle event arrives), so the
// test creates exactly that state without racing a real turn.
func TestDeleteProjectReconcilesAStaleRunningSession(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("stale@example.test", "pw", contracts.RoleUser)
	ctx := context.Background()
	project, session := env.mkSessionFor(user.ID, "stale-running")
	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(ctx, user.ID, session.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := env.store.CreateRun(ctx, user.ID, store.NewRun{SessionID: session.ID, Trigger: "user"}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	// The runtime registry is the authority. An empty registry is what a restart
	// leaves behind; saying so explicitly keeps the test from depending on the
	// fake runtime's default.
	env.runtime.markDead(session.ID)

	creds := env.login("stale@example.test", "pw")
	rec := env.doAuthed(http.MethodDelete, "/v1/projects/"+project.ID, nil, creds)
	if rec.Code != http.StatusOK {
		// On the pre-fix code this is 409 and the row is still "running": the lie
		// is what pinned the project, so name both facts in the failure.
		after, err := env.store.Session(ctx, user.ID, session.ID)
		status := "<gone>"
		if err == nil {
			status = string(after.Status)
		}
		t.Fatalf("delete answered %d, not 200: %s (session status after the refusal: %s)",
			rec.Code, rec.Body.String(), status)
	}
	if _, err := env.store.Project(ctx, user.ID, project.ID); err == nil {
		t.Fatal("the project still exists after a 200 delete")
	}
}

// TestDeleteProjectStillRefusesALiveRun is the other half: reconciliation must
// not become a way to kill work a person is watching. The registry says the
// session is alive, so the refusal stands.
func TestDeleteProjectStillRefusesALiveRun(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("busy@example.test", "pw", contracts.RoleUser)
	ctx := context.Background()
	project, session := env.mkSessionFor(user.ID, "really-running")
	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(ctx, user.ID, session.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	env.runtime.markAlive(session.ID)

	creds := env.login("busy@example.test", "pw")
	rec := env.doAuthed(http.MethodDelete, "/v1/projects/"+project.ID, nil, creds)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete answered %d, want 409 while an agent is alive: %s", rec.Code, rec.Body.String())
	}
	after, err := env.store.Session(ctx, user.ID, session.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if after.Status != contracts.StatusRunning {
		t.Fatalf("a live run was rewritten to %q", after.Status)
	}
	if _, err := env.store.Project(ctx, user.ID, project.ID); err != nil {
		t.Fatalf("a refused delete removed the project: %v", err)
	}
}
