package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
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
	// The limit is configuration now: resolve it through the same function the
	// daemon uses instead of poking an arbitrary number into Deps.
	limit, err := ResolveParallelLimit(1, true, "")
	if err != nil {
		t.Fatalf("resolve limit: %v", err)
	}
	env.deps.ParallelLimit = limit
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
	// The refusal must name the cap and what is occupying it, not just say 429.
	if !strings.Contains(body.Error.Message, "并行上限是 1 个分支") {
		t.Fatalf("the refusal does not name the limit: %q", body.Error.Message)
	}
	if !strings.Contains(body.Error.Message, "hello") {
		t.Fatalf("the refusal does not name what is running: %q", body.Error.Message)
	}
}

// FR-3.3 end to end from configuration: the limit arrives through the resolver,
// two prompts actually start two runs on two branches, and the third branch is
// refused with a message that names the cap and the two branches occupying it.
func TestParallelLimitRefusesTheThirdRun(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, mainSess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, mainSess.ID)

	branchA, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, RootID: mainSess.ID,
		ParentID: mainSess.ID, Label: "branch-a",
	})
	if err != nil {
		t.Fatalf("create branch a: %v", err)
	}
	attachSecondWorktree(t, env, user.ID, project.ID, mainSess.ID, branchA.ID)
	branchB, err := env.store.CreateSession(t.Context(), user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindBranch, RootID: mainSess.ID,
		ParentID: mainSess.ID, Label: "branch-b",
	})
	if err != nil {
		t.Fatalf("create branch b: %v", err)
	}
	attachSecondWorktree(t, env, user.ID, project.ID, mainSess.ID, branchB.ID)

	// Parallel running is opt-in per user (FR-3.2).
	env.mkUser("admin@piforge.local", "pw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "pw")
	if rec := env.doAuthed(http.MethodPost, "/v1/admin/users/"+user.ID+"/features",
		map[string]any{"flag": string(store.FeatureParallel), "enabled": true}, admin); rec.Code != http.StatusOK {
		t.Fatalf("enable parallel: %d %s", rec.Code, rec.Body.String())
	}

	limit, err := ResolveParallelLimit(2, true, "")
	if err != nil {
		t.Fatalf("resolve limit: %v", err)
	}
	env.deps.ParallelLimit = limit
	env.rebuild()
	// Two real prompts through the API, and the fake runtime keeps them running.
	// Start from "nothing is running" so the two prompts below are the runs.
	env.runtime.status = contracts.RuntimeStatus{}
	env.runtime.stayRunning = true
	for _, sessionID := range []string{mainSess.ID, branchA.ID} {
		rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sessionID+"/messages",
			map[string]any{"client_msg_id": "cm_" + sessionID, "text": "hold this run"}, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("start run on %s: %d %s", sessionID, rec.Code, rec.Body.String())
		}
	}
	if got := env.runtime.promptCount(); got != 2 {
		t.Fatalf("expected two runs to have started, got %d", got)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+branchB.ID+"/messages",
		map[string]any{"client_msg_id": "cm_third", "text": "the third one"}, c)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the third run = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Message string         `json:"message"`
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != contracts.ErrRateLimited {
		t.Fatalf("error code = %q", body.Error.Code)
	}
	for _, want := range []string{"并行上限是 2 个分支", "现在有 2 个在运行", "hello", "branch-a"} {
		if !strings.Contains(body.Error.Message, want) {
			t.Fatalf("refusal %q does not contain %q", body.Error.Message, want)
		}
	}
	if got := body.Error.Details["limit"]; got != float64(2) {
		t.Fatalf("details.limit = %v", got)
	}
	running, _ := body.Error.Details["running"].([]any)
	if len(running) != 2 {
		t.Fatalf("details.running = %v", body.Error.Details["running"])
	}
	// The refused branch must not have been prompted.
	if got := env.runtime.promptCount(); got != 2 {
		t.Fatalf("the refused branch still started a run: %d prompts", got)
	}
}
