package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/audit"
)

// auditRecord is one row of audit_log, before and after the chained hash is
// computed. Every stored column is here; chainFields() is the one mapping to
// the hash input, so a new column cannot be forgotten silently (the test in
// audit_chain_test.go compares the two lists).
type auditRecord struct {
	ID            string
	Seq           int64
	SessionID     string
	ToolCallID    string
	Phase         string
	RecordedAt    time.Time
	StartedAt     time.Time
	EndedAt       *time.Time
	UserID        string
	ProjectID     string
	SandboxID     string
	RunID         string
	UserEmail     string
	ProjectName   string
	SessionTitle  string
	Tool          string
	Namespace     string
	ArgsHash      string
	ArgsPreview   string
	PreviewBytes  int
	ArgsTruncated bool
	Status        string
	IsError       *bool
	ExitCode      *int
	DurationMS    *int
	ResultBytes   *int
	ModelRef      string
	CommitSHA     string
	PrevHash      string
	Hash          string
}

func (r auditRecord) chainFields() audit.ChainFields {
	fields := audit.ChainFields{
		ID:            r.ID,
		Seq:           r.Seq,
		SessionID:     r.SessionID,
		ToolCallID:    r.ToolCallID,
		Phase:         r.Phase,
		RecordedAt:    formatTime(r.RecordedAt),
		StartedAt:     formatTime(r.StartedAt),
		UserID:        r.UserID,
		ProjectID:     r.ProjectID,
		SandboxID:     r.SandboxID,
		RunID:         r.RunID,
		UserEmail:     r.UserEmail,
		ProjectName:   r.ProjectName,
		SessionTitle:  r.SessionTitle,
		Tool:          r.Tool,
		Namespace:     r.Namespace,
		ArgsHash:      r.ArgsHash,
		ArgsPreview:   r.ArgsPreview,
		PreviewBytes:  r.PreviewBytes,
		ArgsTruncated: r.ArgsTruncated,
		Status:        r.Status,
		ModelRef:      r.ModelRef,
		CommitSHA:     r.CommitSHA,
	}
	if r.EndedAt != nil {
		fields.EndedAt = formatTime(*r.EndedAt)
	}
	if r.IsError != nil {
		fields.HasIsError = true
		if *r.IsError {
			fields.IsError = 1
		}
	}
	if r.ExitCode != nil {
		fields.HasExitCode = true
		fields.ExitCode = *r.ExitCode
	}
	if r.DurationMS != nil {
		fields.HasDuration = true
		fields.DurationMS = *r.DurationMS
	}
	if r.ResultBytes != nil {
		fields.HasResult = true
		fields.ResultBytes = *r.ResultBytes
	}
	return fields
}

// sessionAuditMeta is the denormalised context written onto every audit row.
type sessionAuditMeta struct {
	UserID       string
	ProjectID    string
	SandboxID    string
	UserEmail    string
	ProjectName  string
	SessionTitle string
	ModelRef     string
}

// auditMetaTx reads the labels and ids an audit row keeps. It runs on the
// write transaction so a row cannot be written against a session that was
// deleted between the check and the insert.
func auditMetaTx(ctx context.Context, tx *sql.Tx, userID, sessionID string) (sessionAuditMeta, error) {
	var meta sessionAuditMeta
	meta.UserID = userID
	err := tx.QueryRowContext(ctx,
		`SELECT s.project_id, COALESCE(s.title, ''), COALESCE(s.model_ref, ''),
		        COALESCE(u.email, ''), COALESCE(p.name, '')
		   FROM sessions s
		   LEFT JOIN users u ON u.id = s.user_id
		   LEFT JOIN projects p ON p.id = s.project_id
		  WHERE s.id = ? AND s.user_id = ?`,
		sessionID, userID).Scan(&meta.ProjectID, &meta.SessionTitle, &meta.ModelRef,
		&meta.UserEmail, &meta.ProjectName)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, fmt.Errorf("store: audit meta for session %q: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return meta, fmt.Errorf("store: audit meta for session %q: %w", sessionID, err)
	}
	// A sidecar shares its main session's worktree; an ephemeral session may
	// have none. An empty sandbox id is a fact about the call, not a failure.
	_ = tx.QueryRowContext(ctx,
		`SELECT COALESCE(w.sandbox_id, p.id)
		   FROM sessions s
		   LEFT JOIN worktrees w ON w.id = s.worktree_id
		   LEFT JOIN sandboxes p ON p.project_id = s.project_id AND p.user_id = s.user_id
		  WHERE s.id = ?`, sessionID).Scan(&meta.SandboxID)
	return meta, nil
}

// appendAuditRow allocates the next per-session sequence, links the row to the
// previous one, and inserts it. Replaying the same (tool call, phase) is a
// no-op: the primary key is deterministic, so a retried event pump cannot fork
// the chain.
func appendAuditRow(ctx context.Context, tx *sql.Tx, rec auditRecord) error {
	if rec.ID == "" {
		rec.ID = rec.ToolCallID + ":" + rec.Phase
	}
	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT seq FROM audit_log WHERE id = ?`, rec.ID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: audit row %q: %w", rec.ID, err)
	}

	var (
		lastSeq  sql.NullInt64
		lastHash sql.NullString
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT seq, hash FROM audit_log WHERE session_id = ? ORDER BY seq DESC LIMIT 1`,
		rec.SessionID).Scan(&lastSeq, &lastHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: audit chain tail for session %q: %w", rec.SessionID, err)
	}
	rec.Seq = lastSeq.Int64 + 1
	if lastHash.Valid {
		rec.PrevHash = lastHash.String
	}
	if rec.RecordedAt.IsZero() {
		rec.RecordedAt = time.Now()
	}
	rec.Hash = audit.ChainHash(rec.PrevHash, rec.chainFields())
	_, err = tx.ExecContext(ctx,
		`INSERT INTO audit_log (
			id, seq, session_id, tool_call_id, phase, recorded_at, started_at, ended_at,
			user_id, project_id, sandbox_id, run_id, user_email, project_name, session_title,
			tool, namespace, args_hash, args_preview, preview_bytes, args_truncated,
			status, is_error, exit_code, duration_ms, result_bytes,
			model_ref, commit_sha, prev_hash, hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Seq, rec.SessionID, rec.ToolCallID, rec.Phase,
		formatTime(rec.RecordedAt), formatTime(rec.StartedAt), nullTime(rec.EndedAt),
		rec.UserID, rec.ProjectID, rec.SandboxID, rec.RunID,
		rec.UserEmail, rec.ProjectName, rec.SessionTitle,
		rec.Tool, rec.Namespace, rec.ArgsHash, rec.ArgsPreview, rec.PreviewBytes, boolInt(rec.ArgsTruncated),
		rec.Status, nullBool(rec.IsError), nullInt(rec.ExitCode), nullInt(rec.DurationMS), nullInt(rec.ResultBytes),
		rec.ModelRef, rec.CommitSHA, rec.PrevHash, rec.Hash)
	if err != nil {
		return fmt.Errorf("store: insert audit row %q: %w", rec.ID, err)
	}
	return nil
}

// startAuditRowTx records the intent half of one tool call inside an existing
// transaction.
func startAuditRowTx(ctx context.Context, tx *sql.Tx, meta sessionAuditMeta, in ToolCall, started time.Time) error {
	preview := audit.BuildPreview(in.ArgsPreview)
	rec := auditRecord{
		ToolCallID:    in.ID,
		Phase:         "start",
		SessionID:     in.SessionID,
		StartedAt:     started,
		UserID:        meta.UserID,
		ProjectID:     meta.ProjectID,
		SandboxID:     meta.SandboxID,
		RunID:         in.RunID,
		UserEmail:     meta.UserEmail,
		ProjectName:   meta.ProjectName,
		SessionTitle:  meta.SessionTitle,
		Tool:          in.Name,
		Namespace:     in.Namespace,
		ArgsHash:      in.ArgsHash,
		ArgsPreview:   string(preview.JSON),
		PreviewBytes:  preview.Bytes,
		ArgsTruncated: preview.Truncated,
		Status:        "running",
		ModelRef:      in.ModelRef,
		CommitSHA:     in.CommitSHA,
	}
	if rec.ModelRef == "" {
		rec.ModelRef = meta.ModelRef
	}
	return appendAuditRow(ctx, tx, rec)
}

// ensureStartRowTx is the migration path for a call whose start row predates
// this table (or was created by a writer that failed between the two inserts):
// the end row must not be the first link in a session's chain.
func ensureStartRowTx(ctx context.Context, tx *sql.Tx, userID, toolCallID string) error {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM audit_log WHERE tool_call_id = ? AND phase = 'start'`, toolCallID).Scan(&one)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: audit start for %q: %w", toolCallID, err)
	}

	var (
		in        ToolCall
		started   string
		sessionID string
	)
	// The read is on the write connection: a tool call written a moment ago by
	// the event pump must be visible here.
	err = tx.QueryRowContext(ctx,
		`SELECT id, session_id, run_id, name, namespace, args_hash,
		        COALESCE(args_preview, ''), started_at
		   FROM tool_calls WHERE id = ? AND user_id = ?`,
		toolCallID, userID).Scan(&in.ID, &sessionID, &in.RunID, &in.Name, &in.Namespace,
		&in.ArgsHash, &in.ArgsPreview, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: audit start for %q: %w", toolCallID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: audit start for %q: %w", toolCallID, err)
	}
	in.SessionID = sessionID
	meta, err := auditMetaTx(ctx, tx, userID, sessionID)
	if err != nil {
		return err
	}
	return startAuditRowTx(ctx, tx, meta, in, parseTime(started))
}

// finishAuditRowTx records the observation half of one tool call.
func finishAuditRowTx(ctx context.Context, tx *sql.Tx, userID, toolCallID string, in ToolCallFinish, ended time.Time) error {
	if err := ensureStartRowTx(ctx, tx, userID, toolCallID); err != nil {
		return err
	}
	var (
		rec      auditRecord
		started  string
		argsHash string
	)
	err := tx.QueryRowContext(ctx,
		`SELECT session_id, tool, namespace, args_hash, started_at,
		        user_id, project_id, sandbox_id, run_id,
		        user_email, project_name, session_title, model_ref, commit_sha
		   FROM audit_log WHERE tool_call_id = ? AND phase = 'start'`,
		toolCallID).Scan(&rec.SessionID, &rec.Tool, &rec.Namespace, &argsHash, &started,
		&rec.UserID, &rec.ProjectID, &rec.SandboxID, &rec.RunID,
		&rec.UserEmail, &rec.ProjectName, &rec.SessionTitle, &rec.ModelRef, &rec.CommitSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: audit end for %q: %w", toolCallID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: audit end for %q: %w", toolCallID, err)
	}
	rec.ToolCallID = toolCallID
	rec.Phase = "end"
	rec.StartedAt = parseTime(started)
	rec.EndedAt = &ended
	rec.ArgsHash = argsHash
	rec.Status = "ok"
	isError := in.IsError
	if isError {
		rec.Status = "error"
	}
	rec.IsError = &isError
	rec.ExitCode = in.ExitCode
	rec.DurationMS = in.DurationMS
	rec.ResultBytes = in.ResultBytes
	return appendAuditRow(ctx, tx, rec)
}

// BeginAuditRun records the run-level facts a tool row points at. It is
// idempotent: the pump may see run.started more than once after a reattach.
func (s *Store) BeginAuditRun(ctx context.Context, in AuditRun) error {
	if strings.TrimSpace(in.RunID) == "" || strings.TrimSpace(in.SessionID) == "" {
		return fmt.Errorf("store: begin audit run: run and session are required: %w", ErrInvalid)
	}
	started := in.StartedAt
	if started.IsZero() {
		started = s.now()
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin audit run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	meta, err := auditMetaTx(ctx, tx, in.UserID, in.SessionID)
	if err != nil {
		return err
	}
	model := in.ModelRef
	if model == "" {
		model = meta.ModelRef
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO audit_runs (run_id, session_id, user_id, project_id, model_ref,
		                         commit_sha, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'running', ?)
		 ON CONFLICT (run_id, session_id) DO UPDATE SET
		   model_ref = CASE WHEN excluded.model_ref <> '' THEN excluded.model_ref ELSE audit_runs.model_ref END,
		   commit_sha = CASE WHEN excluded.commit_sha <> '' THEN excluded.commit_sha ELSE audit_runs.commit_sha END`,
		in.RunID, in.SessionID, in.UserID, meta.ProjectID, model, in.CommitSHA, formatTime(started))
	if err != nil {
		return fmt.Errorf("store: begin audit run %q: %w", in.RunID, err)
	}
	return tx.Commit()
}

// SettleAuditRun writes the terminal usage of a run. The provider reports
// tokens and cost on the settle event; a provider that reports nothing leaves
// zeroes, which the UI shows as "not reported" rather than inventing a number.
func (s *Store) SettleAuditRun(ctx context.Context, in AuditRunSettle) error {
	if strings.TrimSpace(in.RunID) == "" {
		return fmt.Errorf("store: settle audit run: run is required: %w", ErrInvalid)
	}
	settled := in.SettledAt
	if settled == nil {
		at := s.now()
		settled = &at
	}
	status := in.Status
	if status == "" {
		status = "settled"
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE audit_runs
		    SET status = ?, tokens_in = ?, tokens_out = ?, tokens_cache_r = ?, tokens_cache_w = ?,
		        cost_usd = ?, settled_at = ?
		  WHERE run_id = ? AND session_id = ?`,
		status, in.TokensIn, in.TokensOut, in.TokensCacheR, in.TokensCacheW,
		in.CostUSD, formatTime(*settled), in.RunID, in.SessionID)
	if err != nil {
		return fmt.Errorf("store: settle audit run %q: %w", in.RunID, err)
	}
	if n, aerr := res.RowsAffected(); aerr == nil && n == 0 {
		// A run whose start row never arrived (an older run, or a pump that
		// started mid-run) still gets its usage recorded: the trail is more
		// useful with a late row than with a silent gap.
		_, ierr := s.w.ExecContext(ctx,
			`INSERT INTO audit_runs (run_id, session_id, status, tokens_in, tokens_out,
			                         tokens_cache_r, tokens_cache_w, cost_usd, started_at, settled_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (run_id, session_id) DO UPDATE SET
			   status = excluded.status, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out,
			   tokens_cache_r = excluded.tokens_cache_r, tokens_cache_w = excluded.tokens_cache_w,
			   cost_usd = excluded.cost_usd, settled_at = excluded.settled_at`,
			in.RunID, in.SessionID, status, in.TokensIn, in.TokensOut, in.TokensCacheR,
			in.TokensCacheW, in.CostUSD, formatTime(*settled), formatTime(*settled))
		if ierr != nil {
			return fmt.Errorf("store: settle audit run %q: %w", in.RunID, ierr)
		}
	}
	return nil
}

// AuditRun is the run-level context written when a run starts.
type AuditRun struct {
	RunID     string
	SessionID string
	UserID    string
	ModelRef  string
	CommitSHA string
	StartedAt time.Time
}

// AuditRunSettle is the terminal usage a provider reported for a run.
type AuditRunSettle struct {
	RunID        string
	SessionID    string
	Status       string
	TokensIn     int64
	TokensOut    int64
	TokensCacheR int64
	TokensCacheW int64
	CostUSD      float64
	SettledAt    *time.Time
}

// CloseAbandonedToolCalls gives every call of a finished run that never
// reported an outcome an 'abandoned' end row. Without it, a crash or an
// interrupt leaves a call in the trail as 'running' forever, and a reader
// cannot tell "still working" from "the process died".
//
// It returns how many calls it closed.
func (s *Store) CloseAbandonedToolCalls(ctx context.Context, sessionID, runID string) (int, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, fmt.Errorf("store: close abandoned calls: session is required: %w", ErrInvalid)
	}
	rows, err := s.w.QueryContext(ctx,
		`SELECT tc.id, tc.user_id FROM tool_calls tc
		  WHERE tc.session_id = ? AND tc.run_id = ? AND tc.ended_at IS NULL
		    AND NOT EXISTS (SELECT 1 FROM audit_log a
		                     WHERE a.tool_call_id = tc.id AND a.phase = 'end')`,
		sessionID, runID)
	if err != nil {
		return 0, fmt.Errorf("store: close abandoned calls: %w", err)
	}
	type pending struct{ id, userID string }
	open := make([]pending, 0, 4)
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.userID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: close abandoned calls: %w", err)
		}
		open = append(open, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("store: close abandoned calls: %w", err)
	}
	rows.Close()

	closed := 0
	for _, p := range open {
		at := s.now()
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return closed, fmt.Errorf("store: close abandoned calls: %w", err)
		}
		if err := finishAuditRowTx(ctx, tx, p.userID, p.id, ToolCallFinish{
			IsError: true, EndedAt: &at,
		}, at); err != nil {
			_ = tx.Rollback()
			return closed, err
		}
		// Mark the transcript row too: the panel should not keep saying
		// "running" for a run that has settled.
		if _, err := tx.ExecContext(ctx,
			`UPDATE tool_calls SET is_error = 1, ended_at = ? WHERE id = ? AND user_id = ?`,
			formatTime(at), p.id, p.userID); err != nil {
			_ = tx.Rollback()
			return closed, fmt.Errorf("store: close abandoned call %q: %w", p.id, err)
		}
		if err := tx.Commit(); err != nil {
			return closed, fmt.Errorf("store: close abandoned calls: %w", err)
		}
		closed++
	}
	return closed, nil
}

// PruneAuditBefore removes calls that started before cutoff, both phases
// together, and run facts that started before it.
//
// Deleting a call as a unit is what keeps a verifiable trail: a start row
// without its end (or the reverse) is not "some history went away", it is a
// half-written call. The remaining chain is anchored at its new first row; the
// verify endpoint reports that anchor instead of calling a retention pass
// tampering (docs/AUDIT.md).
func (s *Store) PruneAuditBefore(ctx context.Context, cutoff time.Time) (rows int64, runs int64, err error) {
	if cutoff.IsZero() {
		return 0, 0, fmt.Errorf("store: prune audit: cutoff is required: %w", ErrInvalid)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: prune audit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The watermark is captured before the rows go: without it, verification
	// could not tell a retention prefix from a deleted head (audit_pruned).
	type prunedSession struct {
		sessionID string
		through   int64
		rows      int64
	}
	watermarks := make([]prunedSession, 0, 8)
	markerRows, err := tx.QueryContext(ctx,
		`SELECT session_id, MAX(seq), COUNT(*) FROM audit_log
		  WHERE tool_call_id IN (SELECT tool_call_id FROM audit_log
		                          WHERE phase = 'start' AND started_at < ?)
		  GROUP BY session_id`, formatTime(cutoff))
	if err != nil {
		return 0, 0, fmt.Errorf("store: prune audit watermarks: %w", err)
	}
	for markerRows.Next() {
		var p prunedSession
		if err := markerRows.Scan(&p.sessionID, &p.through, &p.rows); err != nil {
			markerRows.Close()
			return 0, 0, fmt.Errorf("store: prune audit watermarks: %w", err)
		}
		watermarks = append(watermarks, p)
	}
	if err := markerRows.Err(); err != nil {
		markerRows.Close()
		return 0, 0, fmt.Errorf("store: prune audit watermarks: %w", err)
	}
	markerRows.Close()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM audit_log
		  WHERE tool_call_id IN (SELECT tool_call_id FROM audit_log
		                          WHERE phase = 'start' AND started_at < ?)`,
		formatTime(cutoff))
	if err != nil {
		return 0, 0, fmt.Errorf("store: prune audit rows: %w", err)
	}
	rows, _ = res.RowsAffected()

	now := formatTime(s.now())
	for _, p := range watermarks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO audit_pruned (session_id, pruned_through_seq, pruned_rows, pruned_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (session_id) DO UPDATE SET
			   pruned_through_seq = MAX(audit_pruned.pruned_through_seq, excluded.pruned_through_seq),
			   pruned_rows = audit_pruned.pruned_rows + excluded.pruned_rows,
			   pruned_at = excluded.pruned_at`,
			p.sessionID, p.through, p.rows, now); err != nil {
			return 0, 0, fmt.Errorf("store: record audit prune watermark: %w", err)
		}
	}

	res, err = tx.ExecContext(ctx,
		`DELETE FROM audit_runs WHERE started_at < ?`, formatTime(cutoff))
	if err != nil {
		return 0, 0, fmt.Errorf("store: prune audit runs: %w", err)
	}
	runs, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("store: prune audit: %w", err)
	}
	return rows, runs, nil
}

// nullBool and nullInt write SQL NULL for an absent observation, so a reader
// can tell "no exit code" from "exit code 0".
func nullBool(v *bool) any {
	if v == nil {
		return nil
	}
	return boolInt(*v)
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// scanArgsPreview is shared by the verification walk, which reads whole rows
// rather than the folded projection.
func scanAuditRecord(sc interface{ Scan(dest ...any) error }) (auditRecord, error) {
	var (
		rec         auditRecord
		recorded    string
		started     string
		ended       sql.NullString
		isError     sql.NullInt64
		exitCode    sql.NullInt64
		duration    sql.NullInt64
		resultBytes sql.NullInt64
		truncated   int
	)
	if err := sc.Scan(
		&rec.ID, &rec.Seq, &rec.SessionID, &rec.ToolCallID, &rec.Phase,
		&recorded, &started, &ended,
		&rec.UserID, &rec.ProjectID, &rec.SandboxID, &rec.RunID,
		&rec.UserEmail, &rec.ProjectName, &rec.SessionTitle,
		&rec.Tool, &rec.Namespace, &rec.ArgsHash, &rec.ArgsPreview,
		&rec.PreviewBytes, &truncated,
		&rec.Status, &isError, &exitCode, &duration, &resultBytes,
		&rec.ModelRef, &rec.CommitSHA, &rec.PrevHash, &rec.Hash,
	); err != nil {
		return rec, err
	}
	rec.RecordedAt = parseTime(recorded)
	rec.StartedAt = parseTime(started)
	rec.ArgsTruncated = truncated == 1
	if ended.Valid {
		t := parseTime(ended.String)
		rec.EndedAt = &t
	}
	if isError.Valid {
		v := isError.Int64 == 1
		rec.IsError = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		rec.ExitCode = &v
	}
	if duration.Valid {
		v := int(duration.Int64)
		rec.DurationMS = &v
	}
	if resultBytes.Valid {
		v := int(resultBytes.Int64)
		rec.ResultBytes = &v
	}
	return rec, nil
}

// marshalPreview is a small helper for a caller that needs the stored preview
// as JSON without another query.
func marshalPreview(raw string) json.RawMessage {
	if raw == "" {
		return nil
	}
	return json.RawMessage(raw)
}
