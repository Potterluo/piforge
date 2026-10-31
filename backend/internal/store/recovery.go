package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/recovery"
)

// InterruptionRecord is the event-log answer to "was this session's last run
// interrupted, and what was it doing?".
//
// The cause and the sentence come from the event log, never from the session
// row: a row says "interrupted", three different things produce that, and the
// user is owed the difference. LastRequest is the latest user message before the
// interruption, which is what the 继续 follow-up has to name.
type InterruptionRecord struct {
	Seq         int64
	RunID       string
	TS          time.Time
	Cause       string
	Reason      string
	LastRequest string
	// Continued reports that a run started after the interruption, which means
	// the 继续 action is already history and must not be offered again.
	Continued bool
}

// LastInterruption returns the newest interruption in one session's event log.
//
// Three event shapes are accepted because three code paths wrote them over time:
//
//   - run.interrupted (this feature's marker, with an explicit cause);
//   - platform.error with code run_interrupted (the reconciler's visible error);
//   - run.settled with status interrupted (the terminal, written by the
//     reconciler before the visible events).
//
// Taking the newest of them means an old session is still explained and a new
// one uses the explicit cause. A session that was never interrupted returns
// ErrNotFound.
func (s *Store) LastInterruption(ctx context.Context, userID, sessionID string) (*InterruptionRecord, error) {
	if _, err := s.Session(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	var (
		rec     InterruptionRecord
		payload string
		ts      string
	)
	err := s.r.QueryRowContext(ctx,
		`SELECT e.seq, e.run_id, COALESCE(e.payload, ''), e.ts
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		  WHERE e.session_id = ? AND s.user_id = ?
		    AND (
		         e.type = ?
		         OR (e.type = ? AND COALESCE(json_extract(e.payload, '$.code'), '') = 'run_interrupted')
		         OR (e.type = ? AND COALESCE(json_extract(e.payload, '$.status'), '') = 'interrupted')
		        )
		  ORDER BY e.seq DESC
		  LIMIT 1`,
		sessionID, userID,
		"run.interrupted", "platform.error", "run.settled").Scan(&rec.Seq, &rec.RunID, &payload, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: last interruption for %q: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: last interruption for %q: %w", sessionID, err)
	}
	rec.TS = parseTime(ts)

	var body struct {
		Cause   string `json:"cause"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if payload != "" && json.Unmarshal([]byte(payload), &body) == nil {
		rec.Cause = body.Cause
		rec.Reason = body.Reason
		if rec.Reason == "" {
			rec.Reason = body.Message
		}
	}
	if rec.Cause == "" {
		// A record from before the cause field: the exact server sentence is
		// evidence, an unrecognised sentence stays unknown.
		rec.Cause = recovery.CauseFromReason(rec.Reason)
	}

	if err := s.readLastRequest(ctx, userID, sessionID, &rec); err != nil {
		return nil, err
	}
	if err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events
		  WHERE session_id = ? AND seq > ? AND type = 'run.started'`,
		sessionID, rec.Seq).Scan(&rec.Continued); err != nil {
		return nil, fmt.Errorf("store: last interruption for %q: continued: %w", sessionID, err)
	}
	return &rec, nil
}

// readLastRequest fills rec.LastRequest from the transcript, falling back to the
// durable messages table when events were pruned.
//
// The event log is preferred because it is what the user saw; the messages table
// is the fallback that survives retention. An empty answer is kept empty: the
// caller refuses to compose a "continue" that cannot say what to continue.
func (s *Store) readLastRequest(ctx context.Context, userID, sessionID string, rec *InterruptionRecord) error {
	var payload string
	err := s.r.QueryRowContext(ctx,
		`SELECT e.payload
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		  WHERE e.session_id = ? AND s.user_id = ? AND e.seq < ? AND e.type = 'message.final'
		    AND COALESCE(json_extract(e.payload, '$.role'), '') = 'user'
		  ORDER BY e.seq DESC
		  LIMIT 1`,
		sessionID, userID, rec.Seq).Scan(&payload)
	switch {
	case err == nil:
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(payload), &body) == nil && body.Text != "" {
			rec.LastRequest = body.Text
			return nil
		}
	case errors.Is(err, sql.ErrNoRows):
		// Fall through to the messages table.
	default:
		return fmt.Errorf("store: last interruption for %q: last request: %w", sessionID, err)
	}

	var text sql.NullString
	err = s.r.QueryRowContext(ctx,
		`SELECT text_body FROM messages
		  WHERE session_id = ? AND user_id = ? AND created_at <= ?
		  ORDER BY created_at DESC
		  LIMIT 1`,
		sessionID, userID, formatTime(rec.TS)).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: last interruption for %q: last request: %w", sessionID, err)
	}
	if text.Valid {
		rec.LastRequest = text.String
	}
	return nil
}
