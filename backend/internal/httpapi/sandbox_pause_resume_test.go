package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// recordPauseResume is a sandbox provider that records the id it was handed.
//
// The defect it exists for was a boundary mismatch, not provider behaviour:
// pause passed `sandbox.ID(user, project)` = `sbx_<hash>` while the container
// is `piforge-<hash>`, so the runner looked for `piforge-sbx_<hash>` and
// answered 422 `sandbox: not found` while the sandbox was READY. A test that
// asserted "Pause was called" would have passed before the fix; this one
// asserts *which id* crossed the boundary.
type recordPauseResume struct {
	contracts.SandboxProvider
	paused  []string
	resumed []string
}

func (r *recordPauseResume) Pause(_ context.Context, sandboxID string) error {
	r.paused = append(r.paused, sandboxID)
	return nil
}

func (r *recordPauseResume) Resume(_ context.Context, sandboxID string) (*contracts.Sandbox, error) {
	r.resumed = append(r.resumed, sandboxID)
	return &contracts.Sandbox{ID: sandboxID, State: contracts.SandboxReady}, nil
}

// attachPauseSandbox gives a session a sandbox whose row id is the provider's
// id — a container name for the docker provider. The pair id would be different
// (`sbx_` + the same hash), which is exactly what used to be passed instead.
func attachPauseSandbox(t *testing.T, env *testEnv, userID string, project *contracts.Project, session *contracts.Session) string {
	t.Helper()
	ctx := context.Background()
	containerID := "piforge-" + strings.Repeat("a1b2c3d4", 2) // 16 hex characters, like containerName
	if _, err := env.store.CreateSandbox(ctx, userID, store.NewSandbox{
		ID:        containerID,
		ProjectID: project.ID,
		State:     contracts.SandboxReady,
		Provider:  "docker",
		RootDir:   sandbox.ContainerWorkspace,
	}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: containerID,
		Path:      sandbox.ContainerWorkspace + "/repo",
		Branch:    "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, session.ID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}
	if pairID := sandbox.ID(userID, project.ID); pairID == containerID {
		t.Fatalf("the test needs two distinct ids; both are %q", containerID)
	}
	return containerID
}

// TestSandboxPauseAndResumeUseTheRowID is the regression for the 422: both
// calls must hand the provider the id the row carries (the container name for a
// container provider), never the pair id that happens to share a hash with it.
func TestSandboxPauseAndResumeUseTheRowID(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(user.ID, "pausable")
	containerID := attachPauseSandbox(t, env, user.ID, project, session)

	recorder := &recordPauseResume{}
	env.deps.Sandbox = recorder
	env.rebuild()
	client := env.login("dev@piforge.local", "pw")

	resp := env.doAuthed("POST", "/v1/sessions/"+session.ID+"/sandbox/pause", nil, client)
	if resp.Code != 200 {
		t.Fatalf("pause = %d: %s", resp.Code, resp.Body.String())
	}
	if len(recorder.paused) != 1 || recorder.paused[0] != containerID {
		t.Fatalf("pause reached the provider with %v, want [%s]; the pair id is %s",
			recorder.paused, containerID, sandbox.ID(user.ID, project.ID))
	}

	resp = env.doAuthed("POST", "/v1/sessions/"+session.ID+"/sandbox/resume", nil, client)
	if resp.Code != 200 {
		t.Fatalf("resume = %d: %s", resp.Code, resp.Body.String())
	}
	if len(recorder.resumed) != 1 || recorder.resumed[0] != containerID {
		t.Fatalf("resume reached the provider with %v, want [%s]", recorder.resumed, containerID)
	}

	// The row's state must follow, or the sandbox page keeps showing the old one.
	sb, err := env.store.Sandbox(context.Background(), user.ID, containerID)
	if err != nil {
		t.Fatalf("read the sandbox row: %v", err)
	}
	if sb.State != contracts.SandboxReady {
		t.Fatalf("state after resume = %q, want %q", sb.State, contracts.SandboxReady)
	}
}
