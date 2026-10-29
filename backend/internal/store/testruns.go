package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TestRun is one test command's outcome, derived from a session's own tool
// events (an agent bash call) or from a command the user typed in the terminal.
//
// It is a table rather than a JSON sidecar because it must survive a reload and
// a process restart, is queried newest-first per session, and is deleted with
// its session by the same foreign-key cascade as every other derived row. A
// sidecar would need its own locking, its own durability story and a second way
// to answer "which sessions exist" — three things the database already does.
type TestRun struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	RunID      string `json:"run_id,omitempty"`
	ToolCallID string `json:"-"`
	// Source is "agent" (a tool call) or "terminal" (typed in the PTY).
	Source string `json:"source"`
	// Runner is the canonical runner label ("go test", "pytest", ...).
	Runner string `json:"runner"`
	// Command is the exact command line that was recognised.
	Command string `json:"command"`
	// Status is running | passed | failed | unrecognized.
	Status     string        `json:"status"`
	Total      int           `json:"total"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Skipped    int           `json:"skipped"`
	DurationMS int64         `json:"duration_ms"`
	Failures   []TestFailure `json:"failures"`
	OutputTail string        `json:"output_tail,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	EndedAt    *time.Time    `json:"ended_at,omitempty"`
}

// TestFailure is one failing test, as the panel renders it: a name, an optional
// assertion location to open in the editor, and the runner's message.
type TestFailure struct {
	Name    string `json:"name"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message,omitempty"`
}

// Test run status values. `unrecognized` is a first-class outcome, not an
// error: the command was a recognised runner's, but its output could not be
// read, so the panel shows 未识别格式 and the raw tail instead of numbers.
const (
	TestRunRunning      = "running"
	TestRunPassed       = "passed"
	TestRunFailed       = "failed"
	TestRunUnrecognized = "unrecognized"
)

// NewTestRun starts a run. The row is written before the command's output is
// parsed, which is what lets the panel show 运行中 while the tests are still
// running.
type NewTestRun struct {
	ID         string
	SessionID  string
	RunID      string
	ToolCallID string
	Source     string
	Runner     string
	Command    string
	StartedAt  time.Time
}

// TestRunFinish settles a run with whatever was parsed.
type TestRunFinish struct {
	Status     string
	Total      int
	Passed     int
	Failed     int
	Skipped    int
	DurationMS int64
	Failures   []TestFailure
	OutputTail string
	EndedAt    time.Time
}

// CreateTestRun records the start of a run.
func (s *Store) CreateTestRun(ctx context.Context, userID string, in NewTestRun) (*TestRun, error) {
	if in.SessionID == "" || in.Command == "" {
		return nil, fmt.Errorf("store: create test run: session and command are required: %w", ErrInvalid)
	}
	if _, err := s.Session(ctx, userID, in.SessionID); err != nil {
		return nil, err
	}
	id := in.ID
	if id == "" {
		id = NewID("tst")
	}
	started := in.StartedAt
	if started.IsZero() {
		started = s.now()
	}
	source := in.Source
	if source == "" {
		source = "agent"
	}
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO test_runs (id, user_id, session_id, run_id, tool_call_id, source, runner,
		                        command, status, failures, output_tail, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '', ?)`,
		id, userID, in.SessionID, in.RunID, in.ToolCallID, source, in.Runner,
		in.Command, TestRunRunning, formatTime(started)); err != nil {
		return nil, fmt.Errorf("store: create test run: %w", err)
	}
	return &TestRun{
		ID: id, SessionID: in.SessionID, RunID: in.RunID, ToolCallID: in.ToolCallID,
		Source: source, Runner: in.Runner, Command: in.Command,
		Status: TestRunRunning, Failures: []TestFailure{}, StartedAt: started.UTC(),
	}, nil
}

// FinishTestRun settles one run by id.
func (s *Store) FinishTestRun(ctx context.Context, userID, id string, in TestRunFinish) error {
	if id == "" {
		return fmt.Errorf("store: finish test run: id is required: %w", ErrInvalid)
	}
	if !validTestRunStatus(in.Status) {
		return fmt.Errorf("store: finish test run: status %q: %w", in.Status, ErrInvalid)
	}
	failures := in.Failures
	if failures == nil {
		failures = []TestFailure{}
	}
	raw, err := json.Marshal(failures)
	if err != nil {
		return fmt.Errorf("store: finish test run: encode failures: %w", err)
	}
	ended := in.EndedAt
	if ended.IsZero() {
		ended = s.now()
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE test_runs
		    SET status = ?, total = ?, passed = ?, failed = ?, skipped = ?,
		        duration_ms = ?, failures = ?, output_tail = ?, ended_at = ?
		  WHERE id = ? AND user_id = ?`,
		in.Status, in.Total, in.Passed, in.Failed, in.Skipped,
		in.DurationMS, string(raw), in.OutputTail, formatTime(ended), id, userID)
	if err != nil {
		return fmt.Errorf("store: finish test run: %w", err)
	}
	return requireOneRow(res, "store: finish test run "+id)
}

// FinishTestRunByToolCall settles the run an agent tool call started. It is the
// path EvToolEnded takes, and it tolerates "no row" because a tool call that
// was not a recognised test command never created one.
func (s *Store) FinishTestRunByToolCall(ctx context.Context, userID, sessionID, toolCallID string, in TestRunFinish) error {
	if toolCallID == "" {
		return fmt.Errorf("store: finish test run: tool call is required: %w", ErrInvalid)
	}
	if !validTestRunStatus(in.Status) {
		return fmt.Errorf("store: finish test run: status %q: %w", in.Status, ErrInvalid)
	}
	failures := in.Failures
	if failures == nil {
		failures = []TestFailure{}
	}
	raw, err := json.Marshal(failures)
	if err != nil {
		return fmt.Errorf("store: finish test run: encode failures: %w", err)
	}
	ended := in.EndedAt
	if ended.IsZero() {
		ended = s.now()
	}
	_, err = s.w.ExecContext(ctx,
		`UPDATE test_runs
		    SET status = ?, total = ?, passed = ?, failed = ?, skipped = ?,
		        duration_ms = ?, failures = ?, output_tail = ?, ended_at = ?
		  WHERE session_id = ? AND tool_call_id = ? AND status = 'running'
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = test_runs.session_id AND s.user_id = ?)`,
		in.Status, in.Total, in.Passed, in.Failed, in.Skipped,
		in.DurationMS, string(raw), in.OutputTail, formatTime(ended),
		sessionID, toolCallID, userID)
	if err != nil {
		return fmt.Errorf("store: finish test run by tool call: %w", err)
	}
	return nil
}

// TestRunByToolCall returns the run a tool call started. It is how the tool
// event's output is matched back to the runner that produced it, so the parser
// does not have to be guessed from the output alone.
func (s *Store) TestRunByToolCall(ctx context.Context, userID, sessionID, toolCallID string) (*TestRun, error) {
	if toolCallID == "" {
		return nil, fmt.Errorf("store: test run by tool call: tool call is required: %w", ErrInvalid)
	}
	row := s.r.QueryRowContext(ctx,
		`SELECT id, session_id, run_id, tool_call_id, source, runner, command, status,
		        total, passed, failed, skipped, duration_ms, failures, output_tail,
		        started_at, ended_at
		   FROM test_runs
		  WHERE session_id = ? AND tool_call_id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = test_runs.session_id AND s.user_id = ?)`,
		sessionID, toolCallID, userID)
	run, err := scanTestRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: test run for tool call %q: %w", toolCallID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: test run for tool call %q: %w", toolCallID, err)
	}
	return run, nil
}

// ListTestRuns returns one session's runs newest first. It is the endpoint the
// panel polls, so the result is bounded by limit.
func (s *Store) ListTestRuns(ctx context.Context, userID, sessionID string, limit int) ([]*TestRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, session_id, run_id, tool_call_id, source, runner, command, status,
		        total, passed, failed, skipped, duration_ms, failures, output_tail,
		        started_at, ended_at
		   FROM test_runs
		  WHERE session_id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = test_runs.session_id AND s.user_id = ?)
		  ORDER BY started_at DESC, id DESC
		  LIMIT ?`,
		sessionID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list test runs for %q: %w", sessionID, err)
	}
	defer rows.Close()

	out := make([]*TestRun, 0, 8)
	for rows.Next() {
		run, err := scanTestRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list test runs for %q: %w", sessionID, err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list test runs for %q: %w", sessionID, err)
	}
	return out, nil
}

func scanTestRun(sc interface{ Scan(dest ...any) error }) (*TestRun, error) {
	var (
		run      TestRun
		failures string
		tail     string
		started  string
		ended    sql.NullString
	)
	if err := sc.Scan(&run.ID, &run.SessionID, &run.RunID, &run.ToolCallID, &run.Source,
		&run.Runner, &run.Command, &run.Status, &run.Total, &run.Passed, &run.Failed,
		&run.Skipped, &run.DurationMS, &failures, &tail, &started, &ended); err != nil {
		return nil, err
	}
	run.OutputTail = tail
	run.StartedAt = parseTime(started)
	run.EndedAt = parseTimePtr(ended)
	run.Failures = []TestFailure{}
	if failures != "" {
		// A corrupt row must not make the whole panel fail; the counts and the
		// tail still tell the user what happened.
		if err := json.Unmarshal([]byte(failures), &run.Failures); err != nil {
			run.Failures = []TestFailure{}
		}
	}
	return &run, nil
}

// SettleOrphanTestRuns marks every run left "running" as unrecognized. It is
// called once at boot: the daemon is the only writer, so a running row in a
// fresh process belongs to a command whose output died with the previous one.
// Settling it is honest; leaving it "running" forever would make the panel lie.
func (s *Store) SettleOrphanTestRuns(ctx context.Context, at time.Time) (int64, error) {
	if at.IsZero() {
		at = s.now()
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE test_runs SET status = ?, ended_at = ? WHERE status = 'running'`,
		TestRunUnrecognized, formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("store: settle orphan test runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: settle orphan test runs: %w", err)
	}
	return n, nil
}

func validTestRunStatus(status string) bool {
	switch status {
	case TestRunRunning, TestRunPassed, TestRunFailed, TestRunUnrecognized:
		return true
	}
	return false
}

// TestRunStatusFor turns a parse outcome into the status the panel renders.
// It is here so the terminal tap and the tool-event path cannot disagree.
func TestRunStatusFor(recognized, failedRun, parseFailed bool) string {
	if !recognized {
		return TestRunUnrecognized
	}
	if failedRun || parseFailed {
		return TestRunFailed
	}
	return TestRunPassed
}
