package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// attachSandbox gives sess a sandbox+worktree and returns the sandbox row, so
// the lifecycle handlers can resolve it through the session.
func attachSandbox(t *testing.T, env *testEnv, userID string, project *contracts.Project, sess *contracts.Session) *contracts.Sandbox {
	t.Helper()
	ctx := context.Background()
	sb, err := env.store.CreateSandbox(ctx, userID, store.NewSandbox{
		ProjectID: project.ID, State: contracts.SandboxPaused, Provider: "local",
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID, Path: "/srv/worktrees/" + sess.ID, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sess.ID, store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	return sb
}

// TestSandboxSnapshotsAreListedReadOnly covers the read path the reaper's
// manifests are discoverable through. It deliberately has no write verb: a
// snapshot is produced by the reaper, not by a client.
func TestSandboxSnapshotsAreListedReadOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")
	sb := attachSandbox(t, env, user.ID, project, sess)

	ctx := context.Background()
	row, err := env.store.CreateSnapshot(ctx, user.ID, store.Snapshot{
		SandboxID: sb.ID, Kind: "fs", ContentHash: "abc123",
		SizeBytes: 42, BlobKey: sb.ID + "/20260301T120000.000000000Z/manifest.json",
		GitCommit: "deadbeef",
	})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	c := env.login("dev@piforge.local", "pw")

	path := "/v1/sessions/" + sess.ID + "/sandbox/snapshots"
	rec := env.doAuthed(http.MethodGet, path, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list snapshots = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Snapshots []store.Snapshot `json:"snapshots"`
	}
	env.mustJSON(rec, &body)
	if len(body.Snapshots) != 1 {
		t.Fatalf("snapshots = %+v", body.Snapshots)
	}
	got := body.Snapshots[0]
	if got.ID != row.ID || got.BlobKey != row.BlobKey || got.SizeBytes != 42 || got.GitCommit != "deadbeef" {
		t.Errorf("snapshot = %+v", got)
	}

	// A different tenant cannot read it; the session lookup answers 404, so the
	// endpoint never reveals that the sandbox exists.
	env.mkUser("other@piforge.local", "pw", contracts.RoleUser)
	otherC := env.login("other@piforge.local", "pw")
	rec = env.doAuthed(http.MethodGet, path, nil, otherC)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)

	// It is read-only: no POST/PUT/DELETE is registered.
	rec = env.doAuthed(http.MethodPost, path, map[string]any{}, c)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("POST snapshots = %d, want no write route", rec.Code)
	}
}
