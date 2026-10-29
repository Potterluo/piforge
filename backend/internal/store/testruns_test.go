package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The panel's data is derived, but it must outlive the page: these tests pin
// the table behaviour the handler and the e2e reload check depend on.
func TestTestRunsPersistNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "dev@piforge.local", contracts.RoleUser)
	project := mkProject(t, s, user.ID, "repo")
	sess := mkSession(t, s, user.ID, project.ID, contracts.KindMain)

	first, err := s.CreateTestRun(ctx, user.ID, NewTestRun{
		SessionID: sess.ID, Runner: "go test", Command: "go test ./...",
		ToolCallID: "call_1", StartedAt: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateTestRun: %v", err)
	}
	if err := s.FinishTestRun(ctx, user.ID, first.ID, TestRunFinish{
		Status: TestRunFailed, Total: 3, Passed: 2, Failed: 1, DurationMS: 31,
		Failures:   []TestFailure{{Name: "TestAdd", File: "calc_test.go", Line: 12, Message: "want 4"}},
		OutputTail: "FAIL",
		EndedAt:    time.Date(2025, 1, 1, 10, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("FinishTestRun: %v", err)
	}

	second, err := s.CreateTestRun(ctx, user.ID, NewTestRun{
		SessionID: sess.ID, Runner: "pytest", Command: "pytest -q",
		StartedAt: time.Date(2025, 1, 1, 10, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateTestRun: %v", err)
	}
	if second.Status != TestRunRunning {
		t.Fatalf("a fresh run should be running, got %q", second.Status)
	}

	runs, err := s.ListTestRuns(ctx, user.ID, sess.ID, 20)
	if err != nil {
		t.Fatalf("ListTestRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	if runs[0].ID != second.ID || runs[1].ID != first.ID {
		t.Fatalf("runs are not newest first: %s, %s", runs[0].ID, runs[1].ID)
	}
	if len(runs[1].Failures) != 1 || runs[1].Failures[0].Line != 12 {
		t.Fatalf("failures did not round-trip: %+v", runs[1].Failures)
	}
	if runs[1].DurationMS != 31 || runs[1].Status != TestRunFailed {
		t.Fatalf("summary did not round-trip: %+v", runs[1])
	}
	if runs[0].EndedAt != nil || runs[0].Status != TestRunRunning {
		t.Fatalf("running row = %+v", runs[0])
	}
}

func TestTestRunsAreScopedToTheOwnerAndTheSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner := mkUser(t, s, "owner@piforge.local", contracts.RoleUser)
	other := mkUser(t, s, "other@piforge.local", contracts.RoleUser)
	project := mkProject(t, s, owner.ID, "repo")
	sess := mkSession(t, s, owner.ID, project.ID, contracts.KindMain)

	if _, err := s.CreateTestRun(ctx, owner.ID, NewTestRun{
		SessionID: sess.ID, Runner: "go test", Command: "go test ./...",
	}); err != nil {
		t.Fatalf("CreateTestRun: %v", err)
	}

	if _, err := s.ListTestRuns(ctx, other.ID, sess.ID, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user list error = %v, want ErrNotFound", err)
	}

	// Deleting the session removes its derived runs with it.
	if err := s.DeleteSession(ctx, owner.ID, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	var n int
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM test_runs WHERE session_id = ?`, sess.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("runs survived their session: %d", n)
	}
}

func TestSettleOrphanTestRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "dev@piforge.local", contracts.RoleUser)
	project := mkProject(t, s, user.ID, "repo")
	sess := mkSession(t, s, user.ID, project.ID, contracts.KindMain)

	if _, err := s.CreateTestRun(ctx, user.ID, NewTestRun{
		SessionID: sess.ID, Runner: "go test", Command: "go test ./...",
	}); err != nil {
		t.Fatalf("CreateTestRun: %v", err)
	}
	n, err := s.SettleOrphanTestRuns(ctx, time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("SettleOrphanTestRuns = (%d, %v), want (1, nil)", n, err)
	}
	runs, err := s.ListTestRuns(ctx, user.ID, sess.ID, 20)
	if err != nil || len(runs) != 1 || runs[0].Status != TestRunUnrecognized {
		t.Fatalf("orphan run = %+v, %v", runs, err)
	}
}
