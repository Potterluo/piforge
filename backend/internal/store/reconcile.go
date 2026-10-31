package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// RunningSession is one session whose persisted state claims a turn is in
// flight: either the session row says "running", or a run row is still
// "started", or both.
//
// It is the reconciler's candidate set, not a promise. A restart leaves exactly
// this shape behind while nothing serves it, which is why the caller must ask
// the in-process agent registry before trusting it.
type RunningSession struct {
	SessionID    string
	UserID       string
	ProjectID    string
	LastActiveAt time.Time
}

// runningSessionCols is the projection scanRunningSession expects.
const runningSessionCols = `s.id, s.user_id, s.project_id, s.last_active_at`

// RunningSessions lists every session that claims to be running, across every
// account. It is a service-level sweep for internal/reconcile (boot and
// watchdog), not an HTTP-facing query, so it deliberately has no user
// predicate; it also serves sessions whose run row says "started" while the
// session row no longer says "running" (an interrupted settle that never
// reached SQLite).
func (s *Store) RunningSessions(ctx context.Context) ([]RunningSession, error) {
	return s.queryRunningSessions(ctx, "")
}

// RunningSessionsInProject is RunningSessions narrowed to one project owned by
// userID. The delete guard calls it before deciding that a project is pinned.
func (s *Store) RunningSessionsInProject(ctx context.Context, userID, projectID string) ([]RunningSession, error) {
	if userID == "" || projectID == "" {
		return nil, fmt.Errorf("store: running sessions: user and project are required: %w", ErrInvalid)
	}
	return s.queryRunningSessions(ctx, ` AND s.user_id = ? AND s.project_id = ?`, userID, projectID)
}

func (s *Store) queryRunningSessions(ctx context.Context, extra string, args ...any) ([]RunningSession, error) {
	query := `SELECT ` + runningSessionCols + `
	            FROM sessions s
	           WHERE (s.status = ?
	                  OR EXISTS (SELECT 1 FROM runs r
	                              WHERE r.session_id = s.id AND r.status = 'started'))` + extra + `
	           ORDER BY s.last_active_at ASC`
	args = append([]any{string(contracts.StatusRunning)}, args...)
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: running sessions: %w", err)
	}
	defer rows.Close()

	out := make([]RunningSession, 0, 8)
	for rows.Next() {
		var (
			item     RunningSession
			lastSeen string
		)
		if err := rows.Scan(&item.SessionID, &item.UserID, &item.ProjectID, &lastSeen); err != nil {
			return nil, fmt.Errorf("store: running sessions: %w", err)
		}
		item.LastActiveAt = parseTime(lastSeen)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: running sessions: %w", err)
	}
	return out, nil
}

// InterruptClaim is what one call to InterruptRunningSession changed. A zero
// value (SessionChanged false, no runs) means the row had already been
// reconciled — the idempotent second pass.
type InterruptClaim struct {
	// SessionChanged reports that sessions.status moved running -> interrupted.
	SessionChanged bool
	// RunIDs are the started runs this call settled.
	RunIDs []string
	// EventSeqs are the sequence numbers of the events this call appended, in
	// write order.
	EventSeqs []int64
}

// Changed reports whether the call wrote anything.
func (c InterruptClaim) Changed() bool { return c.SessionChanged || len(c.RunIDs) > 0 }

// InterruptRunningSession records that one session's in-flight run was cut off.
//
// It is the durable half of reconciliation and is deliberately a single
// transaction:
//
//   - the session only moves when it still says "running", so a concurrent
//     settle or a second reconciler pass cannot rewrite a newer state;
//   - only runs still "started" are settled, with interrupted_by/reason so the
//     record says why;
//   - a run.settled event goes into the transcript (it is the only completion
//     signal the UI listens to), a run.interrupted event names the cause for the
//     recovery marker, and one platform.error carries the reason to a person who
//     opens the session later.
//
// A call with nothing left to change writes no event, which is what makes the
// watchdog and the delete guard safe to run as often as they like.
func (s *Store) InterruptRunningSession(ctx context.Context, userID, sessionID, cause, reason string, at time.Time) (InterruptClaim, error) {
	if userID == "" || sessionID == "" {
		return InterruptClaim{}, fmt.Errorf("store: interrupt running session: user and session are required: %w", ErrInvalid)
	}
	if reason == "" {
		return InterruptClaim{}, fmt.Errorf("store: interrupt running session: a reason is required: %w", ErrInvalid)
	}
	if cause == "" {
		return InterruptClaim{}, fmt.Errorf("store: interrupt running session: a cause is required: %w", ErrInvalid)
	}
	if at.IsZero() {
		at = s.now()
	}
	var claim InterruptClaim

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE sessions SET status = ?
			  WHERE id = ? AND user_id = ? AND status = ?`,
			string(contracts.StatusInterrupted), sessionID, userID, string(contracts.StatusRunning))
		if err != nil {
			return fmt.Errorf("store: interrupt session %q: %w", sessionID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			claim.SessionChanged = true
		} else if err != nil {
			return fmt.Errorf("store: interrupt session %q: %w", sessionID, err)
		}

		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM runs WHERE session_id = ? AND status = 'started' ORDER BY started_at ASC`,
			sessionID)
		if err != nil {
			return fmt.Errorf("store: interrupt session %q: list started runs: %w", sessionID, err)
		}
		var runIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("store: interrupt session %q: read run: %w", sessionID, err)
			}
			runIDs = append(runIDs, id)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("store: interrupt session %q: close runs: %w", sessionID, err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: interrupt session %q: list started runs: %w", sessionID, err)
		}
		if !claim.SessionChanged && len(runIDs) == 0 {
			return nil
		}

		settledPayload, err := json.Marshal(map[string]any{
			"status":     "interrupted",
			"reason":     reason,
			"cause":      cause,
			"reconciled": true,
		})
		if err != nil {
			return fmt.Errorf("store: interrupt session %q: marshal settle: %w", sessionID, err)
		}
		for _, runID := range runIDs {
			res, err := tx.ExecContext(ctx,
				`UPDATE runs
				    SET status = 'interrupted', settled_at = ?, interrupted_by = ?, completion_note = ?
				  WHERE id = ? AND status = 'started'`,
				formatTime(at), "reconcile", reason, runID)
			if err != nil {
				return fmt.Errorf("store: interrupt run %q: %w", runID, err)
			}
			if n, err := res.RowsAffected(); err == nil && n == 0 {
				// Someone settled it between the SELECT and this UPDATE.
				continue
			}
			ev := &contracts.Event{
				SessionID: sessionID,
				RunID:     runID,
				Type:      contracts.EvRunSettled,
				Payload:   settledPayload,
				TS:        at,
			}
			seq, err := insertEvent(ctx, tx, ev, at)
			if err != nil {
				return err
			}
			claim.RunIDs = append(claim.RunIDs, runID)
			claim.EventSeqs = append(claim.EventSeqs, seq)
		}

		// The recovery marker. One per session: the user needs one place that
		// says "this run stopped for this reason", not a row per internal step.
		// The run id is the last started run, which is the one the marker is
		// about.
		interruptedPayload, err := json.Marshal(map[string]any{
			"cause":  cause,
			"reason": reason,
		})
		if err != nil {
			return fmt.Errorf("store: interrupt session %q: marshal interruption: %w", sessionID, err)
		}
		interruptedRun := ""
		if len(runIDs) > 0 {
			interruptedRun = runIDs[len(runIDs)-1]
		}
		seq, err := insertEvent(ctx, tx, &contracts.Event{
			SessionID: sessionID,
			RunID:     interruptedRun,
			Type:      contracts.EvRunInterrupted,
			Payload:   interruptedPayload,
			TS:        at,
		}, at)
		if err != nil {
			return err
		}
		claim.EventSeqs = append(claim.EventSeqs, seq)

		// One visible explanation per session, even when no run row survived:
		// the transcript is where a person looks after a restart.
		errorPayload, err := json.Marshal(map[string]any{
			"code":    "run_interrupted",
			"message": reason,
			"reason":  reason,
			"cause":   cause,
		})
		if err != nil {
			return fmt.Errorf("store: interrupt session %q: marshal error event: %w", sessionID, err)
		}
		seq, err = insertEvent(ctx, tx, &contracts.Event{
			SessionID: sessionID,
			Type:      contracts.EvPlatformErr,
			Payload:   errorPayload,
			TS:        at,
		}, at)
		if err != nil {
			return err
		}
		claim.EventSeqs = append(claim.EventSeqs, seq)
		return nil
	})
	if err != nil {
		return InterruptClaim{}, err
	}
	return claim, nil
}
