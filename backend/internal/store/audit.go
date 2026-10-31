package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The operator's audit trail.
//
// `tool_calls` is written around every tool the agent runs (intent before, outcome
// after), but until now the only query over it was "this session's trace": an
// admin asking "what did this account's agents do last Tuesday" had to read the
// database by hand. This is the same rows read across tenants, with the owner,
// the project and the session joined in, filtered and paginated.
//
// What it is not: a security log. It records the tool's *name*, a hash of its
// arguments and a short preview, not the arguments themselves — the preview is
// already what the transcript shows the user (see CreateToolCall). An operator
// gets "who ran what, when, and whether it failed", which is what the acceptance
// pass asked for, and the arguments stay where they were: in the session.

// AuditFilter narrows an audit query. Every field is optional; the zero value is
// "everything, newest first".
type AuditFilter struct {
	UserID    string
	ProjectID string
	SessionID string
	// Tool matches the tool name exactly ("bash", "write"), because a substring
	// match over names makes "who used shell" unanswerable.
	Tool string
	// Failed, when set, keeps only failed (true) or successful (false) calls.
	Failed *bool
	// Running keeps only calls that have not reported an outcome yet.
	Running bool
	// Since and Until bound started_at. Zero means unbounded.
	Since time.Time
	Until time.Time
	Limit int
	// Offset pages through the result. A keyset cursor would be cheaper, but a
	// tool_calls table is a small multiple of the events an operator already
	// keeps, and offset keeps the API (and the export) trivial to reason about.
	Offset int
}

// Normalised applies the defaults and the caps every caller shares.
func (f AuditFilter) Normalised() AuditFilter {
	if f.Limit <= 0 {
		f.Limit = DefaultAuditLimit
	}
	if f.Limit > MaxAuditLimit {
		f.Limit = MaxAuditLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

const (
	// DefaultAuditLimit is one screen of the admin table.
	DefaultAuditLimit = 50
	// MaxAuditLimit bounds one page (and one export page): an unbounded query
	// over every tool call a deployment ever made is a memory problem, not a
	// feature.
	MaxAuditLimit = 500
)

// AuditRow is one tool call with the context an operator needs to act on it.
type AuditRow struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS *int       `json:"duration_ms,omitempty"`
	UserID     string     `json:"user_id"`
	UserEmail  string     `json:"user_email,omitempty"`
	ProjectID  string     `json:"project_id"`
	Project    string     `json:"project,omitempty"`
	SessionID  string     `json:"session_id"`
	Session    string     `json:"session,omitempty"`
	RunID      string     `json:"run_id,omitempty"`
	Tool       string     `json:"tool"`
	Namespace  string     `json:"namespace,omitempty"`
	ArgsHash   string     `json:"args_hash"`
	// Failed is nil while the call is still running (no ended_at yet).
	Failed      *bool `json:"failed,omitempty"`
	ExitCode    *int  `json:"exit_code,omitempty"`
	ResultBytes *int  `json:"result_bytes,omitempty"`
}

// Outcome is the human word for a row: the admin table and the CSV export both
// use it instead of a tri-state boolean.
func (r AuditRow) Outcome() string {
	switch {
	case r.Failed == nil:
		return "running"
	case *r.Failed:
		return "error"
	default:
		return "ok"
	}
}

// auditWhere builds the WHERE clause and its arguments. It is separate from the
// query so the count and the page cannot disagree about what they are counting.
func auditWhere(f AuditFilter) (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if v := strings.TrimSpace(f.UserID); v != "" {
		clauses = append(clauses, "tc.user_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.ProjectID); v != "" {
		clauses = append(clauses, "s.project_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.SessionID); v != "" {
		clauses = append(clauses, "tc.session_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.Tool); v != "" {
		clauses = append(clauses, "tc.name = ?")
		args = append(args, v)
	}
	if f.Failed != nil {
		if *f.Failed {
			clauses = append(clauses, "tc.is_error = 1")
		} else {
			// A call that never finished is not a success: it has no outcome yet,
			// and counting it as ok would hide a wedged run.
			clauses = append(clauses, "tc.is_error = 0", "tc.ended_at IS NOT NULL")
		}
	}
	if f.Running {
		clauses = append(clauses, "tc.ended_at IS NULL")
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "tc.started_at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "tc.started_at <= ?")
		args = append(args, formatTime(f.Until))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// AuditToolCalls returns one page of the audit trail, newest first, plus the
// total number of rows the filter matches.
//
// It takes no caller id on purpose: it is a service-level query like
// RunningSessions, and the HTTP layer is what requires an admin (the store cannot
// tell an admin from a bug).
func (s *Store) AuditToolCalls(ctx context.Context, filter AuditFilter) ([]AuditRow, int, error) {
	filter = filter.Normalised()
	where, args := auditWhere(filter)

	var total int
	countQuery := `SELECT COUNT(*)
	                 FROM tool_calls tc
	                 JOIN sessions s ON s.id = tc.session_id` + where
	if err := s.r.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count audit tool calls: %w", err)
	}

	// Newest first with id as the tie-break: two calls can start inside one
	// millisecond, and an unstable order makes paging lose rows.
	pageQuery := `SELECT tc.id, tc.started_at, tc.ended_at, tc.duration_ms,
	                     tc.user_id, COALESCE(u.email, ''),
	                     s.project_id, COALESCE(p.name, ''),
	                     tc.session_id, COALESCE(s.title, ''),
	                     tc.run_id, tc.name, tc.namespace, tc.args_hash,
	                     tc.is_error, tc.exit_code, tc.result_bytes
	                FROM tool_calls tc
	                JOIN sessions s ON s.id = tc.session_id
	           LEFT JOIN projects p ON p.id = s.project_id
	           LEFT JOIN users u ON u.id = tc.user_id` +
		where + ` ORDER BY tc.started_at DESC, tc.id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)

	rows, err := s.r.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: audit tool calls: %w", err)
	}
	defer rows.Close()

	out := make([]AuditRow, 0, filter.Limit)
	for rows.Next() {
		var (
			row       AuditRow
			started   string
			ended     *string
			duration  *int
			failed    *int
			exitCode  *int
			resultB   *int
			runID     string
			namespace string
		)
		if err := rows.Scan(&row.ID, &started, &ended, &duration,
			&row.UserID, &row.UserEmail,
			&row.ProjectID, &row.Project,
			&row.SessionID, &row.Session,
			&runID, &row.Tool, &namespace, &row.ArgsHash,
			&failed, &exitCode, &resultB); err != nil {
			return nil, 0, fmt.Errorf("store: audit tool calls: %w", err)
		}
		row.StartedAt = parseTime(started)
		if ended != nil {
			t := parseTime(*ended)
			row.EndedAt = &t
		}
		row.DurationMS = duration
		if failed != nil {
			// The column is INTEGER 0/1; the wire field is a boolean so a reader
			// cannot mistake 0 for "unknown".
			f := *failed != 0
			row.Failed = &f
		}
		row.ExitCode = exitCode
		row.ResultBytes = resultB
		row.RunID = runID
		row.Namespace = namespace
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: audit tool calls: %w", err)
	}
	return out, total, nil
}
