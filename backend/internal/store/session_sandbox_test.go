package store

import (
	"context"
	"errors"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The liveness probe starts from a session id and nothing else, so the store has
// to answer "which sandbox holds this session" in one go. The worktree's sandbox
// wins (a sidecar or branch session shares its main session's worktree); a
// session with no worktree falls back to its project's sandbox.
func TestSessionSandboxPrefersTheWorktreeAndFallsBackToTheProject(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "dev@piforge.local", contracts.RoleUser)
	project := mkProject(t, st, user.ID, "p")

	sb, err := st.CreateSandbox(ctx, user.ID, NewSandbox{ProjectID: project.ID, RootDir: "/data/sandboxes/u/p"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	// The row the provider would write: the agent's launcher needs RootDir, and
	// the probe needs the id.
	if sb.Provider != "" {
		t.Fatalf("sandbox provider = %q", sb.Provider)
	}
	wt, err := st.CreateWorktree(ctx, user.ID, NewWorktree{
		SandboxID: sb.ID, Path: "/data/sandboxes/u/p/worktrees/main", Branch: "main", BaseCommit: "abc",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// A session without a worktree: the project's sandbox.
	plain := mkSession(t, st, user.ID, project.ID, contracts.KindMain)
	got, err := st.SessionSandboxFor(ctx, plain.ID)
	if err != nil {
		t.Fatalf("SessionSandboxFor (no worktree): %v", err)
	}
	if got.SandboxID != sb.ID || got.RootDir != "/data/sandboxes/u/p" {
		t.Fatalf("SessionSandboxFor = %+v, want the project's sandbox", got)
	}

	// A session with a worktree: that worktree's sandbox.
	withTree, err := st.CreateSession(ctx, user.ID, NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, Title: "branch", WorktreeID: wt.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err = st.SessionSandboxFor(ctx, withTree.ID)
	if err != nil {
		t.Fatalf("SessionSandboxFor (worktree): %v", err)
	}
	if got.SandboxID != sb.ID {
		t.Fatalf("SessionSandboxFor = %+v, want the worktree's sandbox", got)
	}

	// Unknown or empty ids are errors, which the probe turns into "unknown"
	// rather than "dead".
	if _, err := st.SessionSandboxFor(ctx, "ses_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionSandboxFor(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := st.SessionSandboxFor(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SessionSandboxFor(empty) = %v, want ErrInvalid", err)
	}
}
