package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// FR-12.5: the panel is only useful if the list endpoint answers "no runs yet"
// and "these runs" with the same shape, newest first, and never leaks another
// user's session.
func TestSessionTestRunsEmpty(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/test-runs", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("test-runs: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs  []store.TestRun `json:"runs"`
		Count int             `json:"count"`
	}
	env.mustJSON(rec, &body)
	if body.Count != 0 || len(body.Runs) != 0 {
		t.Fatalf("empty session returned %+v", body)
	}
	// The wire must carry [] rather than null, or the panel cannot tell "empty"
	// from "the field is missing".
	if !jsonArray(rec.Body.Bytes(), "runs") {
		t.Fatalf("runs is not a JSON array: %s", rec.Body.String())
	}
}

func TestSessionTestRunsReturnsParsedRun(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	run, err := env.store.CreateTestRun(context.Background(), user.ID, store.NewTestRun{
		SessionID: sess.ID, Source: "agent", Runner: "go test", Command: "go test ./...",
		ToolCallID: "call_1", StartedAt: time.Date(2025, 2, 1, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateTestRun: %v", err)
	}
	if err := env.store.FinishTestRun(context.Background(), user.ID, run.ID, store.TestRunFinish{
		Status: store.TestRunFailed, Total: 3, Passed: 2, Failed: 1, DurationMS: 42,
		Failures: []store.TestFailure{{
			Name: "TestAdd", File: "calc_test.go", Line: 12, Message: "want 4, got 3",
		}},
		EndedAt: time.Date(2025, 2, 1, 9, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("FinishTestRun: %v", err)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/test-runs", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("test-runs: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs  []store.TestRun `json:"runs"`
		Count int             `json:"count"`
	}
	env.mustJSON(rec, &body)
	if body.Count != 1 || len(body.Runs) != 1 {
		t.Fatalf("count = %d, runs = %d", body.Count, len(body.Runs))
	}
	got := body.Runs[0]
	if got.Status != store.TestRunFailed || got.Total != 3 || got.Failed != 1 || got.DurationMS != 42 {
		t.Fatalf("summary = %+v", got)
	}
	if len(got.Failures) != 1 {
		t.Fatalf("failures = %+v", got.Failures)
	}
	f := got.Failures[0]
	if f.Name != "TestAdd" || f.File != "calc_test.go" || f.Line != 12 || f.Message != "want 4, got 3" {
		t.Fatalf("failure = %+v", f)
	}
}

func TestSessionTestRunsNewestFirstAndBounded(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	base := time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		run, err := env.store.CreateTestRun(context.Background(), user.ID, store.NewTestRun{
			SessionID: sess.ID, Source: "agent", Runner: "go test", Command: "go test ./...",
			ToolCallID: fmt.Sprintf("call_%d", i),
			StartedAt:  base.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateTestRun %d: %v", i, err)
		}
		ids = append(ids, run.ID)
		if err := env.store.FinishTestRun(context.Background(), user.ID, run.ID, store.TestRunFinish{
			Status: store.TestRunPassed, Total: 1, Passed: 1,
			EndedAt: base.Add(time.Duration(i)*time.Minute + time.Second),
		}); err != nil {
			t.Fatalf("FinishTestRun %d: %v", i, err)
		}
	}

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/test-runs?limit=2", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("test-runs: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs  []store.TestRun `json:"runs"`
		Count int             `json:"count"`
	}
	env.mustJSON(rec, &body)
	if body.Count != 2 || len(body.Runs) != 2 {
		t.Fatalf("limit ignored: count = %d, runs = %d", body.Count, len(body.Runs))
	}
	if body.Runs[0].ID != ids[2] || body.Runs[1].ID != ids[1] {
		t.Fatalf("runs are not newest first: %s, %s", body.Runs[0].ID, body.Runs[1].ID)
	}
}

func TestSessionTestRunsAreScopedToTheOwner(t *testing.T) {
	env := newTestEnv(t)
	owner := env.mkUser("owner@piforge.local", "pw", contracts.RoleUser)
	env.mkUser("other@piforge.local", "pw2", contracts.RoleUser)
	_, sess := env.mkSessionFor(owner.ID, "repo")

	other := env.login("other@piforge.local", "pw2")
	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/test-runs", nil, other)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user test-runs = %d, want 404", rec.Code)
	}
}

// jsonArray reports whether the named top-level field is a JSON array. It keeps
// the empty-state assertion about the wire, not about Go's nil-slice quirks.
func jsonArray(body []byte, field string) bool {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	_, ok := raw[field].([]any)
	return ok
}
