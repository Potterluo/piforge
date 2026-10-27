package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// runCols is: id, session_id, trigger, status, tokens_in, tokens_out,
// tokens_cache_r, tokens_cache_w, cost_usd, started_at, settled_at,
// interrupted_by, completion_note.
const runCols = `id, session_id, trigger, status, tokens_in, tokens_out, tokens_cache_r,
	tokens_cache_w, cost_usd, started_at, settled_at, interrupted_by, completion_note`

// NewRun is the input for CreateRun.
type NewRun struct {
	// ID, when set, is the caller's identifier rather than a generated one.
	// The event pump uses the runtime's run id so that run.settled can find
	// the same row.
	ID        string
	SessionID string
	// Trigger is user|sidecar|fork; it defaults to "user".
	Trigger string
}

// RunSettle is the terminal update of a run, written after agent_settled
// (PRD §7.3 durability table).
type RunSettle struct {
	// Status is settled|aborted|failed|interrupted; it defaults to "settled".
	Status         string
	TokensIn       int64
	TokensOut      int64
	TokensCacheR   int64
	TokensCacheW   int64
	CostUSD        float64
	SettledAt      *time.Time
	InterruptedBy  string
	CompletionNote string
}

// CreateRun records the start of a prompt -> agent_settled cycle.
func (s *Store) CreateRun(ctx context.Context, userID string, in NewRun) (*contracts.Run, error) {
	if _, err := s.Session(ctx, userID, in.SessionID); err != nil {
		return nil, err
	}
	trigger := in.Trigger
	if trigger == "" {
		trigger = "user"
	}
	id := in.ID
	if id == "" {
		id = NewID("run")
	}
	now := s.now()
	if _, err := s.w.ExecContext(ctx,
		`INSERT INTO runs (id, session_id, trigger, status, started_at)
		 VALUES (?, ?, ?, 'started', ?)
		 ON CONFLICT (id) DO NOTHING`,
		id, in.SessionID, trigger, formatTime(now)); err != nil {
		return nil, fmt.Errorf("store: create run: %w", err)
	}
	return s.Run(ctx, userID, id)
}

// Run loads one run, scoped through its session's owner.
func (s *Store) Run(ctx context.Context, userID, runID string) (*contracts.Run, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM runs
		  WHERE id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = runs.session_id AND s.user_id = ?)`,
		runID, userID)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: run %q: %w", runID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: run %q: %w", runID, err)
	}
	return r, nil
}

// SettleRun writes the terminal state of a run. It is idempotent: replaying it
// just overwrites the same values, which matters because the settle event and
// the interrupt path can both race to close the same run.
func (s *Store) SettleRun(ctx context.Context, userID, runID string, in RunSettle) error {
	status := in.Status
	if status == "" {
		status = "settled"
	}
	switch status {
	case "settled", "aborted", "failed", "interrupted":
	default:
		return fmt.Errorf("store: settle run %q: unknown status %q: %w", runID, status, ErrInvalid)
	}
	settledAt := in.SettledAt
	if settledAt == nil {
		now := s.now()
		settledAt = &now
	}
	res, err := s.w.ExecContext(ctx,
		`UPDATE runs
		    SET status = ?, tokens_in = ?, tokens_out = ?, tokens_cache_r = ?, tokens_cache_w = ?,
		        cost_usd = ?, settled_at = ?, interrupted_by = ?, completion_note = ?
		  WHERE id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = runs.session_id AND s.user_id = ?)`,
		status, in.TokensIn, in.TokensOut, in.TokensCacheR, in.TokensCacheW,
		in.CostUSD, formatTime(*settledAt), in.InterruptedBy, in.CompletionNote, runID, userID)
	if err != nil {
		return fmt.Errorf("store: settle run %q: %w", runID, err)
	}
	return requireOneRow(res, "store: settle run "+runID)
}

// ListRuns lists a session's runs oldest first, for the context meter.
func (s *Store) ListRuns(ctx context.Context, userID, sessionID string) ([]*contracts.Run, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs
		  WHERE session_id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = runs.session_id AND s.user_id = ?)
		  ORDER BY started_at ASC`,
		sessionID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list runs for %q: %w", sessionID, err)
	}
	defer rows.Close()

	out := make([]*contracts.Run, 0, 8)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list runs for %q: %w", sessionID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list runs for %q: %w", sessionID, err)
	}
	return out, nil
}

func scanRun(sc interface{ Scan(dest ...any) error }) (*contracts.Run, error) {
	var (
		r       contracts.Run
		started string
		settled sql.NullString
	)
	if err := sc.Scan(&r.ID, &r.SessionID, &r.Trigger, &r.Status, &r.TokensIn, &r.TokensOut,
		&r.TokensCacheR, &r.TokensCacheW, &r.CostUSD, &started, &settled,
		&r.InterruptedBy, &r.CompletionNote); err != nil {
		return nil, err
	}
	r.StartedAt = parseTime(started)
	r.SettledAt = parseTimePtr(settled)
	return &r, nil
}

// SessionStats aggregates what the context meter shows (PRD FR-1.8): message
// and tool-call counts plus token totals. Context window occupancy is not
// stored here — the HTTP layer fills it from the last context.usage event.
func (s *Store) SessionStats(ctx context.Context, userID, sessionID string) (*contracts.SessionStats, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	st := &contracts.SessionStats{SessionID: sessionID}

	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = ? AND user_id = ?`,
		sessionID, userID).Scan(&st.UserMessages); err != nil {
		return nil, fmt.Errorf("store: session stats: user messages: %w", err)
	}

	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM events e JOIN sessions s ON s.id = e.session_id
		  WHERE e.session_id = ? AND s.user_id = ?
		    AND e.type = 'message.final'
		    AND COALESCE(json_extract(e.payload, '$.role'), 'assistant') = 'assistant'`,
		sessionID, userID).Scan(&st.AssistantMsgs); err != nil {
		return nil, fmt.Errorf("store: session stats: events: %w", err)
	}

	// Counted from the tool_calls table rather than from events: the table is
	// the durable record and survives event pruning.
	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tool_calls WHERE session_id = ? AND user_id = ?`,
		sessionID, userID).Scan(&st.ToolCalls); err != nil {
		return nil, fmt.Errorf("store: session stats: tool calls: %w", err)
	}

	if err := s.r.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0),
		        COALESCE(SUM(tokens_cache_r), 0), COALESCE(SUM(cost_usd), 0)
		   FROM runs
		  WHERE session_id = ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = runs.session_id AND s.user_id = ?)`,
		sessionID, userID).Scan(&st.TokensIn, &st.TokensOut, &st.TokensCacheR, &st.CostUSD); err != nil {
		return nil, fmt.Errorf("store: session stats: runs: %w", err)
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Messages (client_msg_id idempotency, PRD §7.4.5)
// ---------------------------------------------------------------------------

// MessageRecord is the persisted result of a submitted user message. Replaying
// the same (session, client_msg_id) returns the original record, which is how
// POST /messages stays idempotent across retries and reconnects.
type MessageRecord struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	ClientMsgID   string    `json:"client_msg_id"`
	Text          string    `json:"text"`
	Accepted      bool      `json:"accepted"`
	Disposition   string    `json:"disposition"`
	QueuePosition int       `json:"queue_position"`
	RunID         string    `json:"run_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// SaveMessage records a submitted message keyed by (session_id, client_msg_id)
// and reports whether this call created the row. A false "created" means the
// message had already been accepted and the returned record is the original
// one, so the caller must return it instead of prompting the agent again.
func (s *Store) SaveMessage(ctx context.Context, userID, sessionID, clientMsgID, text string, res contracts.PromptResult) (*MessageRecord, bool, error) {
	if clientMsgID == "" {
		return nil, false, fmt.Errorf("store: save message: client_msg_id is required: %w", ErrInvalid)
	}
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, false, err
	}
	id := NewID("msg")
	accepted := 0
	if res.Accepted {
		accepted = 1
	}
	r, err := s.w.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, user_id, client_msg_id, text_body, accepted,
		                       disposition, queue_position, run_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id, client_msg_id) DO NOTHING`,
		id, sessionID, userID, clientMsgID, text, accepted, res.Disposition,
		res.QueuePosition, res.RunID, formatTime(s.now()))
	if err != nil {
		return nil, false, fmt.Errorf("store: save message: %w", err)
	}
	created := false
	if n, err := r.RowsAffected(); err == nil && n > 0 {
		created = true
	}
	rec, err := s.Message(ctx, userID, sessionID, clientMsgID)
	if err != nil {
		return nil, false, err
	}
	return rec, created, nil
}

// Message loads one previously accepted message by its client id.
func (s *Store) Message(ctx context.Context, userID, sessionID, clientMsgID string) (*MessageRecord, error) {
	row := s.r.QueryRowContext(ctx,
		`SELECT id, session_id, client_msg_id, text_body, accepted, disposition,
		        queue_position, run_id, created_at
		   FROM messages WHERE session_id = ? AND client_msg_id = ? AND user_id = ?`,
		sessionID, clientMsgID, userID)
	var (
		rec      MessageRecord
		accepted int
		created  string
	)
	err := row.Scan(&rec.ID, &rec.SessionID, &rec.ClientMsgID, &rec.Text, &accepted,
		&rec.Disposition, &rec.QueuePosition, &rec.RunID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: message %q: %w", clientMsgID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: message %q: %w", clientMsgID, err)
	}
	rec.Accepted = accepted == 1
	rec.CreatedAt = parseTime(created)
	return &rec, nil
}
