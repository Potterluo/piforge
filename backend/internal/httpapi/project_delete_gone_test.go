package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// goneSandbox is a provider that has already released every sandbox it is asked
// to destroy. That is exactly the state the idle reaper leaves behind: the
// provider-side sandbox is gone while the row (and its snapshot rows) stays
// readable, so the project must still be deletable.
type goneSandbox struct {
	contracts.SandboxProvider
	attempts int
}

func (g *goneSandbox) Destroy(_ context.Context, id string) error {
	g.attempts++
	return fmt.Errorf("%w: %s", sandbox.ErrNotFound, id)
}

// A project whose sandbox was reclaimed is deletable; before this, the delete
// answered 409 forever ("沙箱目录删不掉"), so the project and its rows could never
// go away.
func TestDeleteProjectSucceedsWhenTheSandboxWasAlreadyReclaimed(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "reclaimed")
	sb := attachSandbox(t, env, user.ID, project, sess)
	if _, err := env.store.CreateSnapshot(context.Background(), user.ID,
		store.Snapshot{SandboxID: sb.ID, ContentHash: "h"}); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	gone := &goneSandbox{}
	env.deps.Sandbox = gone
	env.rebuild()

	rec := env.doAuthed(http.MethodDelete, "/v1/projects/"+project.ID, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s (a provider that no longer knows the sandbox must be a no-op)", rec.Code, rec.Body.String())
	}
	if gone.attempts != 1 {
		t.Fatalf("destroy attempts = %d, want the not-found answer accepted on the first try", gone.attempts)
	}
	if _, err := env.store.Project(context.Background(), user.ID, project.ID); err == nil {
		t.Fatal("the project row survived a successful delete")
	}
	rows, err := env.store.SnapshotIndex(context.Background())
	if err != nil {
		t.Fatalf("SnapshotIndex: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("snapshot rows outlived the project: %+v", rows)
	}
}
