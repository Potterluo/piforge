package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The panel's rows come from the same events the transcript is built from.
// These tests pin the derivation end to end: a bash call that runs a test
// command creates a running row, and the tool result settles it — including the
// case where the output cannot be read.
func TestToolEventsDeriveATestRun(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")
	hub := newHub(env.store, env.runtime, env.deps.Logger)

	started := contracts.Event{
		SessionID: sess.ID,
		RunID:     "run_1",
		Type:      contracts.EvToolStarted,
		TS:        time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC),
		Payload:   json.RawMessage(`{"tool_call_id":"call_1","name":"bash","args":{"command":"go test ./..."}}`),
	}
	hub.applyDerivedState(user.ID, started)

	runs, err := env.store.ListTestRuns(context.Background(), user.ID, sess.ID, 10)
	if err != nil {
		t.Fatalf("ListTestRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != store.TestRunRunning {
		t.Fatalf("running row = %+v", runs)
	}
	if runs[0].Source != "agent" || runs[0].Runner != "go test" {
		t.Fatalf("row = %+v", runs[0])
	}

	ended := contracts.Event{
		SessionID: sess.ID,
		RunID:     "run_1",
		Type:      contracts.EvToolEnded,
		TS:        time.Date(2025, 4, 1, 10, 0, 1, 0, time.UTC),
		Payload: json.RawMessage(`{"tool_call_id":"call_1","is_error":true,"duration_ms":1240,` +
			`"text":"--- FAIL: TestAdd (0.00s)\n\tcalc_test.go:12: want 4\nFAIL\n"}`),
	}
	hub.applyDerivedState(user.ID, ended)

	runs, err = env.store.ListTestRuns(context.Background(), user.ID, sess.ID, 10)
	if err != nil {
		t.Fatalf("ListTestRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if run.Status != store.TestRunFailed || run.Failed != 1 || run.Total != 1 {
		t.Fatalf("settled row = %+v", run)
	}
	// The parser has no duration for this truncated output; the tool's own
	// duration is the honest fallback.
	if run.DurationMS != 1240 {
		t.Fatalf("duration = %d, want the tool duration", run.DurationMS)
	}
	if len(run.Failures) != 1 || run.Failures[0].File != "calc_test.go" || run.Failures[0].Line != 12 {
		t.Fatalf("failures = %+v", run.Failures)
	}
}

func TestToolEventsRecordUnreadableOutputAsUnrecognized(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")
	hub := newHub(env.store, env.runtime, env.deps.Logger)

	hub.applyDerivedState(user.ID, contracts.Event{
		SessionID: sess.ID, Type: contracts.EvToolStarted, TS: time.Now(),
		Payload: json.RawMessage(`{"tool_call_id":"call_2","name":"bash","args":{"command":"make test"}}`),
	})
	hub.applyDerivedState(user.ID, contracts.Event{
		SessionID: sess.ID, Type: contracts.EvToolEnded, TS: time.Now(),
		Payload: json.RawMessage(`{"tool_call_id":"call_2","is_error":true,"duration_ms":30,` +
			`"text":"make: *** [Makefile:2: test] Error 1\n"}`),
	})

	runs, err := env.store.ListTestRuns(context.Background(), user.ID, sess.ID, 10)
	if err != nil {
		t.Fatalf("ListTestRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != store.TestRunUnrecognized {
		t.Fatalf("row = %+v", runs)
	}
	if runs[0].Total != 0 || runs[0].Failed != 0 {
		t.Fatalf("unrecognized run invented counts: %+v", runs[0])
	}
	if runs[0].OutputTail == "" {
		t.Fatal("the raw tail was not kept")
	}
}

func TestNonTestCommandsCreateNoRun(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")
	hub := newHub(env.store, env.runtime, env.deps.Logger)

	for _, command := range []string{"git status", "go build ./...", "ls -la"} {
		payload, _ := json.Marshal(map[string]any{
			"tool_call_id": "call_" + command,
			"name":         "bash",
			"args":         map[string]string{"command": command},
		})
		hub.applyDerivedState(user.ID, contracts.Event{
			SessionID: sess.ID, Type: contracts.EvToolStarted, TS: time.Now(),
			Payload: payload,
		})
	}

	runs, err := env.store.ListTestRuns(context.Background(), user.ID, sess.ID, 10)
	if err != nil {
		t.Fatalf("ListTestRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("non-test commands became runs: %+v", runs)
	}
}
