package store

import (
	"context"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The disk watchdog asks "which sandboxes have a run in flight" once per pass, and
// the answer decides whether that sandbox is measured every two seconds or every
// ten. The query has to follow the same rows the UI does: a session with a
// worktree in a sandbox, whose status is running.
func TestRunningSandboxIDsFollowsTheStatusAndTheWorktree(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "running@piforge.local", contracts.RoleUser)

	busyProject := mkProject(t, st, user.ID, "busy")
	idleProject := mkProject(t, st, user.ID, "idle")
	busySandbox, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: busyProject.ID, State: contracts.SandboxReady})
	if err != nil {
		t.Fatalf("CreateSandbox(busy): %v", err)
	}
	idleSandbox, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: idleProject.ID, State: contracts.SandboxReady})
	if err != nil {
		t.Fatalf("CreateSandbox(idle): %v", err)
	}
	busyWT, err := st.CreateWorktree(ctx, user.ID, NewWorktree{SandboxID: busySandbox.ID, Path: "/workspace/wt-busy", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateWorktree(busy): %v", err)
	}
	idleWT, err := st.CreateWorktree(ctx, user.ID, NewWorktree{SandboxID: idleSandbox.ID, Path: "/workspace/wt-idle", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateWorktree(idle): %v", err)
	}

	busy := mkSession(t, st, user.ID, busyProject.ID, contracts.KindMain)
	idle := mkSession(t, st, user.ID, idleProject.ID, contracts.KindMain)
	running := contracts.SessionStatus("running")
	for _, pair := range []struct {
		id string
		wt string
	}{{busy.ID, busyWT.ID}, {idle.ID, idleWT.ID}} {
		p := pair
		if _, err := st.UpdateSession(ctx, user.ID, p.id, SessionPatch{WorktreeID: &p.wt, Status: &running}); err != nil {
			t.Fatalf("UpdateSession: %v", err)
		}
	}

	ids, err := st.RunningSandboxIDs(ctx)
	if err != nil {
		t.Fatalf("RunningSandboxIDs: %v", err)
	}
	if !ids[busySandbox.ID] || !ids[idleSandbox.ID] {
		t.Fatalf("both sandboxes have a running session, got %v", ids)
	}

	// One settles, and only the other stays in the set.
	idleStatus := contracts.SessionStatus("idle")
	if _, err := st.UpdateSession(ctx, user.ID, idle.ID, SessionPatch{Status: &idleStatus}); err != nil {
		t.Fatalf("UpdateSession(idle): %v", err)
	}
	ids, err = st.RunningSandboxIDs(ctx)
	if err != nil {
		t.Fatalf("RunningSandboxIDs: %v", err)
	}
	if !ids[busySandbox.ID] {
		t.Fatalf("the busy sandbox dropped out of the running set: %v", ids)
	}
	if ids[idleSandbox.ID] {
		t.Fatalf("a sandbox whose only session settled is still reported running: %v", ids)
	}
}
