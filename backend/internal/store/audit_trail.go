package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The forensic trail (docs/AUDIT.md).
//
// `tool_calls` is the transcript's Trace: it cascades with its session and is
// what the user reads. `audit_log` is the operator's copy, and it is written
// here so every path that records a tool call also records the audit row — the
// event pump, a test that seeds the store, and any future caller. There is no
// second write site to forget.
//
// A call is two append-only rows:
//
//	start  intent: tool, bounded redacted preview, ids, sandbox, model, commit
//	end    observation: status, exit code, duration, result bytes
//
// Append-only is what makes the chained hash stable: nothing rewrites a row
// after its hash is computed, so an edited row is detectable.

// AuditRow is one tool call assembled from its two rows plus the run facts an
// investigator needs beside it. It is the wire shape of /v1/admin/audit.
type AuditRow struct {
	ID         string     `json:"id"`
	Seq        int64      `json:"seq"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS *int       `json:"duration_ms,omitempty"`
	UserID     string     `json:"user_id"`
	UserEmail  string     `json:"user_email,omitempty"`
	ProjectID  string     `json:"project_id"`
	Project    string     `json:"project,omitempty"`
	SessionID  string     `json:"session_id"`
	Session    string     `json:"session,omitempty"`
	SandboxID  string     `json:"sandbox_id,omitempty"`
	RunID      string     `json:"run_id,omitempty"`
	Tool       string     `json:"tool"`
	Namespace  string     `json:"namespace,omitempty"`
	ArgsHash   string     `json:"args_hash"`
	// ArgsPreview is the bounded, redacted JSON object stored at call start.
	// It is never the raw arguments.
	ArgsPreview      json.RawMessage `json:"args_preview,omitempty"`
	PreviewBytes     int             `json:"preview_bytes"`
	PreviewTruncated bool            `json:"preview_truncated,omitempty"`
	// Status is running|ok|error|abandoned.
	Status string `json:"status"`
	// Failed is kept for the shape the UI already reads: nil while running,
	// true for an error (or an abandoned call), false for ok.
	Failed      *bool `json:"failed,omitempty"`
	ExitCode    *int  `json:"exit_code,omitempty"`
	ResultBytes *int  `json:"result_bytes,omitempty"`
	// ModelRef and CommitSHA are what was true when the call started.
	ModelRef  string `json:"model_ref,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	// The run facts are joined from audit_runs, so they survive the session
	// that produced them. Zero means the provider reported nothing.
	RunModelRef     string  `json:"run_model_ref,omitempty"`
	RunTokensIn     int64   `json:"run_tokens_in"`
	RunTokensOut    int64   `json:"run_tokens_out"`
	RunTokensCacheR int64   `json:"run_tokens_cache_read"`
	RunTokensCacheW int64   `json:"run_tokens_cache_write"`
	RunCostUSD      float64 `json:"run_cost_usd"`
	// The chain fields are exposed so an export can be verified off-line.
	PrevHash string `json:"prev_hash,omitempty"`
	Hash     string `json:"hash"`
}

// Outcome is the human word for a row. The stored status is authoritative; the
// tri-state boolean is only a convenience for older readers.
func (r AuditRow) Outcome() string {
	if r.Status != "" {
		return r.Status
	}
	switch {
	case r.Failed == nil:
		return "running"
	case *r.Failed:
		return "error"
	default:
		return "ok"
	}
}

// AuditFilter narrows an audit query. Every field is optional; the zero value
// is "everything, newest first".
type AuditFilter struct {
	UserID    string
	ProjectID string
	SessionID string
	// Tool matches the tool name exactly ("bash", "write"), because a substring
	// match over names makes "who used shell" unanswerable.
	Tool string
	// Status keeps one outcome: ok, error, running, abandoned. Empty means all.
	Status string
	// Failed is the older tri-state spelling, kept because the UI sends it.
	Failed *bool
	// Running keeps only calls that have not reported an outcome yet.
	Running bool
	// Since and Until bound started_at. Zero means unbounded.
	Since time.Time
	Until time.Time
	// MinDurationMS drops calls faster than this. A running call has no
	// duration, so it is dropped too: "slower than X" is a question about
	// observations, not intent.
	MinDurationMS int
	// Search is a case-insensitive substring over the stored preview only.
	// It is not a search over the session: the full arguments are not here.
	Search string
	Limit  int
	// Offset pages through the result. Keyset paging would be cheaper, but a
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
	if f.MinDurationMS < 0 {
		f.MinDurationMS = 0
	}
	return f
}

const (
	// DefaultAuditLimit is one screen of the admin table.
	DefaultAuditLimit = 50
	// MaxAuditLimit bounds one page. An unbounded query over every tool call a
	// deployment ever made is a memory problem, not a feature.
	MaxAuditLimit = 500
	// AuditExportBatch is how many rows one streaming export holds in Go at a
	// time. The reader pulls from SQLite row by row; this is the bound a test
	// asserts on so "streaming" cannot quietly become "collect and write".
	AuditExportBatch = 500
)

// auditCallsCTE assembles one logical call from its start and end rows.
//
// The start row carries everything that describes intent; the end row carries
// the observation. MAX(CASE ...) is how the two are folded without a correlated
// subquery per call. Filters are applied on the folded row, so a filter on
// outcome sees the end row even when it is paged with a start row.
const auditCallsCTE = `WITH calls AS (
	SELECT
		session_id,
		tool_call_id AS call_id,
		MAX(CASE WHEN phase = 'start' THEN seq END)          AS seq,
		MAX(CASE WHEN phase = 'start' THEN started_at END)   AS started_at,
		MAX(CASE WHEN phase = 'end'   THEN ended_at END)     AS ended_at,
		MAX(CASE WHEN phase = 'end'   THEN duration_ms END)  AS duration_ms,
		MAX(CASE WHEN phase = 'start' THEN user_id END)      AS user_id,
		MAX(CASE WHEN phase = 'start' THEN user_email END)   AS user_email,
		MAX(CASE WHEN phase = 'start' THEN project_id END)   AS project_id,
		MAX(CASE WHEN phase = 'start' THEN project_name END) AS project_name,
		MAX(CASE WHEN phase = 'start' THEN session_title END) AS session_title,
		MAX(CASE WHEN phase = 'start' THEN sandbox_id END)   AS sandbox_id,
		MAX(CASE WHEN phase = 'start' THEN run_id END)       AS run_id,
		MAX(CASE WHEN phase = 'start' THEN tool END)         AS tool,
		MAX(CASE WHEN phase = 'start' THEN namespace END)    AS namespace,
		MAX(CASE WHEN phase = 'start' THEN args_hash END)    AS args_hash,
		MAX(CASE WHEN phase = 'start' THEN args_preview END) AS args_preview,
		MAX(CASE WHEN phase = 'start' THEN preview_bytes END) AS preview_bytes,
		MAX(CASE WHEN phase = 'start' THEN args_truncated END) AS args_truncated,
		MAX(CASE WHEN phase = 'start' THEN model_ref END)    AS model_ref,
		MAX(CASE WHEN phase = 'start' THEN commit_sha END)   AS commit_sha,
		MAX(CASE WHEN phase = 'start' THEN prev_hash END)    AS prev_hash,
		MAX(CASE WHEN phase = 'start' THEN hash END)         AS hash,
		MAX(CASE WHEN phase = 'end'   THEN status END)       AS end_status,
		MAX(CASE WHEN phase = 'end'   THEN is_error END)     AS is_error,
		MAX(CASE WHEN phase = 'end'   THEN exit_code END)    AS exit_code,
		MAX(CASE WHEN phase = 'end'   THEN result_bytes END) AS result_bytes
	FROM audit_log
	GROUP BY session_id, tool_call_id
)`

// auditWhere builds the post-fold WHERE clause and its arguments. Keeping the
// count and the page on one builder is what stops "1–50 of 412" from lying.
func auditWhere(f AuditFilter) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	if v := strings.TrimSpace(f.UserID); v != "" {
		clauses = append(clauses, "c.user_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.ProjectID); v != "" {
		clauses = append(clauses, "c.project_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.SessionID); v != "" {
		clauses = append(clauses, "c.session_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.Tool); v != "" {
		clauses = append(clauses, "c.tool = ?")
		args = append(args, v)
	}
	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "":
		if f.Failed != nil {
			if *f.Failed {
				clauses = append(clauses, "c.end_status IN ('error', 'abandoned')")
			} else {
				clauses = append(clauses, "c.end_status = 'ok'")
			}
		}
	case "ok":
		clauses = append(clauses, "c.end_status = 'ok'")
	case "error", "failed":
		clauses = append(clauses, "c.end_status = 'error'")
	case "abandoned":
		clauses = append(clauses, "c.end_status = 'abandoned'")
	case "running":
		clauses = append(clauses, "c.end_status IS NULL")
	default:
		// The HTTP layer validates; an unknown status here would silently return
		// everything, which is worse than an empty page.
		clauses = append(clauses, "1 = 0")
	}
	if f.Running {
		clauses = append(clauses, "c.end_status IS NULL")
	}
	if f.MinDurationMS > 0 {
		clauses = append(clauses, "c.duration_ms >= ?")
		args = append(args, f.MinDurationMS)
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		// INSTR is a literal substring match: a preview containing % or _ is
		// searched for those characters, not treated as a wildcard.
		clauses = append(clauses, "INSTR(LOWER(c.args_preview), LOWER(?)) > 0")
		args = append(args, q)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "c.started_at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "c.started_at <= ?")
		args = append(args, formatTime(f.Until))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// auditSelect is the column list the page, the export and the stream share.
const auditSelect = `SELECT
	c.call_id, c.seq, c.started_at, c.ended_at, c.duration_ms,
	c.user_id, c.user_email, c.project_id, c.project_name,
	c.session_id, c.session_title, c.sandbox_id, c.run_id,
	c.tool, c.namespace, c.args_hash, c.args_preview,
	c.preview_bytes, c.args_truncated,
	c.end_status, c.exit_code, c.result_bytes,
	c.model_ref, c.commit_sha, c.prev_hash, c.hash,
	COALESCE(r.tokens_in, 0), COALESCE(r.tokens_out, 0),
	COALESCE(r.tokens_cache_r, 0), COALESCE(r.tokens_cache_w, 0),
	COALESCE(r.cost_usd, 0), COALESCE(r.model_ref, '')
	  FROM calls c
	  LEFT JOIN audit_runs r ON r.run_id = c.run_id AND r.session_id = c.session_id`

// AuditToolCalls returns one page of the audit trail, newest first, plus the
// total number of calls the filter matches.
//
// It takes no caller id on purpose: it is a service-level query, and the HTTP
// layer is what requires an admin (the store cannot tell an admin from a bug).
func (s *Store) AuditToolCalls(ctx context.Context, filter AuditFilter) ([]AuditRow, int, error) {
	filter = filter.Normalised()
	where, args := auditWhere(filter)

	var total int
	countQuery := auditCallsCTE + ` SELECT COUNT(*) FROM calls c` + where
	if err := s.r.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count audit calls: %w", err)
	}

	pageQuery := auditCallsCTE + ` ` + auditSelect + where +
		` ORDER BY c.started_at DESC, c.call_id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.r.QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: audit calls: %w", err)
	}
	defer rows.Close()

	out := make([]AuditRow, 0, filter.Limit)
	for rows.Next() {
		row, err := scanAuditRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: audit calls: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: audit calls: %w", err)
	}
	return out, total, nil
}

// StreamAuditCalls walks every call the filter matches, newest first, without
// materialising them. The callback is called once per row, in order.
//
// The export handlers use this: 100k rows must not become 100k AuditRow values
// in the process's heap. A callback error stops the walk and is returned; the
// caller decides what a half-written download means.
func (s *Store) StreamAuditCalls(ctx context.Context, filter AuditFilter, fn func(AuditRow) error) error {
	filter = filter.Normalised()
	// Paging is deliberately ignored: an export is the whole filtered set.
	where, args := auditWhere(filter)
	query := auditCallsCTE + ` ` + auditSelect + where +
		` ORDER BY c.started_at DESC, c.call_id DESC`
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: stream audit calls: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		row, err := scanAuditRow(rows)
		if err != nil {
			return fmt.Errorf("store: stream audit calls: %w", err)
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: stream audit calls: %w", err)
	}
	return nil
}

func scanAuditRow(sc interface{ Scan(dest ...any) error }) (AuditRow, error) {
	var (
		row         AuditRow
		started     string
		ended       sql.NullString
		duration    sql.NullInt64
		preview     sql.NullString
		truncated   int
		endStatus   sql.NullString
		exitCode    sql.NullInt64
		resultBytes sql.NullInt64
	)
	if err := sc.Scan(
		&row.ID, &row.Seq, &started, &ended, &duration,
		&row.UserID, &row.UserEmail, &row.ProjectID, &row.Project,
		&row.SessionID, &row.Session, &row.SandboxID, &row.RunID,
		&row.Tool, &row.Namespace, &row.ArgsHash, &preview,
		&row.PreviewBytes, &truncated,
		&endStatus, &exitCode, &resultBytes,
		&row.ModelRef, &row.CommitSHA, &row.PrevHash, &row.Hash,
		&row.RunTokensIn, &row.RunTokensOut,
		&row.RunTokensCacheR, &row.RunTokensCacheW,
		&row.RunCostUSD, &row.RunModelRef,
	); err != nil {
		return row, err
	}
	row.StartedAt = parseTime(started)
	if ended.Valid {
		t := parseTime(ended.String)
		row.EndedAt = &t
	}
	if duration.Valid {
		v := int(duration.Int64)
		row.DurationMS = &v
	}
	if preview.Valid && preview.String != "" {
		row.ArgsPreview = json.RawMessage(preview.String)
	}
	row.PreviewTruncated = truncated == 1
	if endStatus.Valid {
		row.Status = endStatus.String
		if row.Status != "ok" {
			failed := true
			row.Failed = &failed
		} else {
			failed := false
			row.Failed = &failed
		}
	} else {
		row.Status = "running"
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		row.ExitCode = &v
	}
	if resultBytes.Valid {
		v := int(resultBytes.Int64)
		row.ResultBytes = &v
	}
	return row, nil
}
