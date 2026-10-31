package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// AppendEvent stores one normalized event, allocating the next sequence number
// for its session atomically, and returns a copy carrying the assigned Seq.
//
// The allocation is part of the INSERT (INSERT ... SELECT
// COALESCE(MAX(seq),0)+1 ... RETURNING seq) and runs inside a transaction on
// the single-connection write pool, so two concurrent producers for the same
// session can never hand out the same seq. That matters because seq is the SSE
// event id and therefore the client's replay cursor (PRD §7.4.4).
func (s *Store) AppendEvent(ctx context.Context, userID string, ev *contracts.Event) (*contracts.Event, error) {
	if ev == nil || ev.SessionID == "" || ev.Type == "" {
		return nil, fmt.Errorf("store: append event: session and type are required: %w", ErrInvalid)
	}
	ts := ev.TS
	if ts.IsZero() {
		ts = s.now()
	}
	if ev.Payload != nil && !json.Valid(ev.Payload) {
		return nil, fmt.Errorf("store: append event %q: payload is not valid JSON: %w", ev.Type, ErrInvalid)
	}

	var seq int64

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var kind string
		err := tx.QueryRowContext(ctx,
			`SELECT kind FROM sessions WHERE id = ? AND user_id = ?`,
			ev.SessionID, userID).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: append event: session %q: %w", ev.SessionID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("store: append event: session %q: %w", ev.SessionID, err)
		}

		if seq, err = insertEvent(ctx, tx, ev, ts); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET last_active_at = ? WHERE id = ? AND user_id = ?`,
			formatTime(ts), ev.SessionID, userID); err != nil {
			return fmt.Errorf("store: append event: touch session: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := *ev
	out.Seq = seq
	out.TS = ts
	return &out, nil
}

// insertEvent writes one event row, allocating the session's next seq inside
// the caller's transaction. It is shared by AppendEvent and the reconciler so a
// recovery event is ordered by exactly the same rule as a live one.
func insertEvent(ctx context.Context, tx *sql.Tx, ev *contracts.Event, ts time.Time) (int64, error) {
	var payload any
	if len(ev.Payload) > 0 {
		payload = string(ev.Payload)
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO events (session_id, seq, run_id, entry_id, parent_id, type, payload, ts)
		 SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?, ?
		   FROM events WHERE session_id = ?
		 RETURNING seq`,
		ev.SessionID, ev.RunID, ev.EntryID, ev.ParentID, ev.Type, payload,
		formatTime(ts), ev.SessionID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: append event %q: %w", ev.Type, err)
	}
	return seq, nil
}

// ListEvents returns events of one session with seq > afterSeq in ascending
// order. This is the SSE replay path, so the result is always bounded: callers
// page forward using the seq of the last event they received.
func (s *Store) ListEvents(ctx context.Context, userID, sessionID string, afterSeq int64, limit int) ([]*contracts.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}

	rows, err := s.r.QueryContext(ctx,
		`SELECT e.session_id, e.seq, e.run_id, e.entry_id, e.parent_id, e.type,
		        COALESCE(e.payload, ''), e.ts, s.kind
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		  WHERE e.session_id = ? AND e.seq > ? AND s.user_id = ?
		  ORDER BY e.seq ASC LIMIT ?`,
		sessionID, afterSeq, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events for %q: %w", sessionID, err)
	}
	defer rows.Close()

	out := make([]*contracts.Event, 0, limit)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list events for %q: %w", sessionID, err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events for %q: %w", sessionID, err)
	}
	return out, nil
}

// EventSeqRange reports the lowest and highest seq retained for a session, or
// (0, 0) when it has no events. The SSE handler uses it to detect a replay gap
// and answer 409 need_full_resync instead of silently losing events.
func (s *Store) EventSeqRange(ctx context.Context, userID, sessionID string) (minSeq, maxSeq int64, err error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return 0, 0, err
	}
	var lo, hi sql.NullInt64
	err = s.r.QueryRowContext(ctx,
		`SELECT MIN(e.seq), MAX(e.seq)
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		  WHERE e.session_id = ? AND s.user_id = ?`,
		sessionID, userID).Scan(&lo, &hi)
	if err != nil {
		return 0, 0, fmt.Errorf("store: event seq range for %q: %w", sessionID, err)
	}
	return lo.Int64, hi.Int64, nil
}

// PruneEvents deletes events with seq <= upto for one session and reports how
// many rows were removed. It implements the hot/cold retention of PRD §8.3 and
// is also what creates the replay gap that the SSE endpoint reports.
func (s *Store) PruneEvents(ctx context.Context, userID, sessionID string, upto int64) (int64, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return 0, err
	}
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM events
		  WHERE session_id = ? AND seq <= ?
		    AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = events.session_id AND s.user_id = ?)`,
		sessionID, upto, userID)
	if err != nil {
		return 0, fmt.Errorf("store: prune events for %q: %w", sessionID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune events for %q: %w", sessionID, err)
	}
	return n, nil
}

func scanEvent(sc interface{ Scan(dest ...any) error }) (*contracts.Event, error) {
	var (
		ev      contracts.Event
		kind    string
		payload string
		ts      string
	)
	if err := sc.Scan(&ev.SessionID, &ev.Seq, &ev.RunID, &ev.EntryID, &ev.ParentID,
		&ev.Type, &payload, &ts, &kind); err != nil {
		return nil, err
	}
	ev.Kind = contracts.SessionKind(kind)
	// The session is the branch: one Pi process per session (PRD §7.4.1), so
	// branch_id mirrors session_id until a separate branch identity exists.
	ev.BranchID = ev.SessionID
	ev.TS = parseTime(ts)
	if payload != "" {
		ev.Payload = json.RawMessage(payload)
	}
	return &ev, nil
}

// LastContextUsage reads the newest context.usage event payload and returns
// its context_tokens/context_window pair. It reports ok=false when the session
// has no such event yet, which is the normal state before the first turn
// settles.
//
// It reads the whole transcript, not the session's own log: a fresh fork has
// inherited every event of its parent but written none, and a meter that drops
// to zero the moment a branch is created is a number the agent does not agree
// with (its Pi session was forked with the same context).
func (s *Store) LastContextUsage(ctx context.Context, userID, sessionID string) (tokens, window int64, ok bool, err error) {
	payload, found, err := s.lastMergedPayload(ctx, userID, sessionID, contracts.EvContextUsage)
	if err != nil || !found {
		return 0, 0, false, err
	}
	var body struct {
		ContextTokens int64 `json:"context_tokens"`
		ContextWindow int64 `json:"context_window"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		return 0, 0, false, nil
	}
	if body.ContextTokens == 0 && body.ContextWindow == 0 {
		return 0, 0, false, nil
	}
	return body.ContextTokens, body.ContextWindow, true, nil
}
