package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// FR-3.1–3.3: who gets to run when several branches of one session want to.
//
// The default is one at a time and the others are *paused*, not silently left
// running; parallel mode exists but is opt-in and has a limit that says why it
// refused.
func TestConcurrencyPolicy(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, main := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, main.ID)

	branch, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, RootID: main.ID, ParentID: main.ID,
	})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	attachSecondWorktree(t, env, user.ID, project.ID, main.ID, branch.ID)

	// The sibling is running.
	env.runtime.status = contracts.RuntimeStatus{Running: true, ProcessID: 1234}

	// FR-3.1: a prompt on the other branch pauses it first.
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+branch.ID+"/messages",
		map[string]any{"client_msg_id": "cm_1", "text": "run here"}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("prompt: %d %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	interrupts := append([]string(nil), env.runtime.interrupted...)
	env.runtime.mu.Unlock()
	if len(interrupts) == 0 || interrupts[0] != main.ID {
		t.Fatalf("the sibling was not paused: %v", interrupts)
	}
	// And the pause is visible in the event log, not just in the runtime.
	paused, err := env.store.ListEvents(t.Context(), user.ID, main.ID, 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, event := range paused {
		if event.Type == "session.paused" {
			found = true
		}
	}
	if !found {
		t.Fatal("no session.paused event was recorded")
	}

	// FR-3.2/3.3: with parallel on, a third branch is refused at the limit instead of
	// pausing anyone.
	admin := env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	adminClient := env.login("admin@piforge.local", "pw")
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/users/"+user.ID+"/features",
		map[string]any{"flag": string(store.FeatureParallel), "enabled": true}, adminClient); rec.Code != http.StatusOK {
		t.Fatalf("enable parallel: %d %s", rec.Code, rec.Body.String())
	}
	_ = admin
	// The limit lives in deps at construction time, so rebuild the router with it.
	env.deps.ParallelLimit = 1
	env.rebuild()
	env.runtime.status = contracts.RuntimeStatus{Running: true, ProcessID: 1234}
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+branch.ID+"/messages",
		map[string]any{"client_msg_id": "cm_2", "text": "run in parallel"}, c)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("past the limit = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Message == "" {
		t.Fatal("the refusal has no reason")
	}
}
