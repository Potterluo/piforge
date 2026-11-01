package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Gateway token revocations, persisted (docs/EXECUTION-BOUNDARY.md §14.3).
//
// The model gateway signs a small claim instead of storing the token, so
// "revoke" is a generation counter: every token carries the generation it was
// minted under, and the verifier refuses a token whose number is not the
// session's current one. Until this table existed the counter lived only in the
// control plane's memory, and the consequence is the one adversarial acceptance
// run #2 recorded: a control-plane restart inside the token's one-hour lifetime
// re-enabled every token minted before it, because the restarted process
// started counting from zero again.
//
// The rules this table has to keep:
//
//   - a revocation is written in the same call that ends the token (not queued,
//     not asynchronously), so "pause returned 200" means "the row is on disk";
//   - the generation only ever grows, so a token minted for the session's *next*
//     spawn (a resume after a pause) carries the new number and works;
//   - the row is swept at `expires_at`, which is the expiry of the last token the
//     revocation could still have affected — after that the token's own `exp`
//     claim has done the work;
//   - issuing a token for a session that already has a revocation extends that
//     `expires_at` (see ExtendGatewayRevocation), or the sweep could delete the
//     row while a *new* token still relies on its generation.

// GatewayRevocation is one session's durable revocation record.
type GatewayRevocation struct {
	SessionID  string
	Generation int64
	RevokedAt  time.Time
	ExpiresAt  time.Time
}

// RevokeGatewayToken bumps one session's revocation generation and returns the
// new number. It is idempotent in effect: calling it twice for the same stop
// yields generations 1 then 2, and a token minted after either call carries the
// current value, so the only tokens it invalidates are the ones already handed
// out.
//
// expiresAt is when the last token this revocation can affect expires. A caller
// that does not know (there is always a TTL) should pass now+TTL; a zero is
// treated as "now", which only means the sweep may collect the row on its next
// pass.
//
// The read-modify-write is one transaction: the write pool is a single
// connection (see the package comment), and two concurrent stops of the same
// session must not both read generation 0 and both write 1.
func (s *Store) RevokeGatewayToken(ctx context.Context, sessionID string, expiresAt time.Time) (int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, fmt.Errorf("store: revoke gateway token: empty session id: %w", ErrInvalid)
	}
	now := s.now()
	if expiresAt.IsZero() || expiresAt.Before(now) {
		expiresAt = now
	}
	var generation int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := gatewayGenerationTx(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		generation = current + 1
		_, err = tx.ExecContext(ctx,
			`INSERT INTO gateway_revocations (session_id, generation, revoked_at, expires_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(session_id) DO UPDATE SET
			     generation = excluded.generation,
			     revoked_at = excluded.revoked_at,
			     expires_at = excluded.expires_at`,
			sessionID, generation, formatTime(now), formatTime(expiresAt))
		if err != nil {
			return fmt.Errorf("store: revoke gateway token: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}

// GatewayRevocation returns the durable record for one session. ok is false
// when nothing was ever revoked for it (generation 0), which is the ordinary
// case and must not be an error.
func (s *Store) GatewayRevocation(ctx context.Context, sessionID string) (GatewayRevocation, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return GatewayRevocation{}, false, fmt.Errorf("store: gateway revocation: empty session id: %w", ErrInvalid)
	}
	var (
		gen                int64
		revokedAt, expiring string
	)
	err := s.r.QueryRowContext(ctx,
		`SELECT generation, revoked_at, expires_at FROM gateway_revocations WHERE session_id = ?`,
		sessionID).Scan(&gen, &revokedAt, &expiring)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayRevocation{}, false, nil
	}
	if err != nil {
		return GatewayRevocation{}, false, fmt.Errorf("store: gateway revocation: %w", err)
	}
	return GatewayRevocation{
		SessionID:  sessionID,
		Generation: gen,
		RevokedAt:  parseTime(revokedAt),
		ExpiresAt:  parseTime(expiring),
	}, true, nil
}

// GatewayRevocationGeneration is the number a token for this session must carry
// right now: 0 when nothing was revoked, the stored generation otherwise. It is
// what the gateway's verifier compares against on every model call, so it is a
// single indexed primary-key read and returns no error for the common case.
func (s *Store) GatewayRevocationGeneration(ctx context.Context, sessionID string) (int64, error) {
	rev, ok, err := s.GatewayRevocation(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return rev.Generation, nil
}

// ExtendGatewayRevocation pushes one session's revocation expiry out to
// expiresAt, without touching its generation.
//
// It exists because of the sweep. A resumed session mints a token *after* the
// revoke, and that token's own expiry is later than the revocation row's: if the
// sweep collected the row at the old deadline, the token would be rejected for
// the remainder of its life because the verifier would read generation 0 while
// the token carries generation 1. Issuing a token therefore keeps its basis
// alive. A session with no revocation row is a no-op (there is nothing to
// extend and nothing to sweep).
func (s *Store) ExtendGatewayRevocation(ctx context.Context, sessionID string, expiresAt time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if expiresAt.IsZero() {
		return nil
	}
	_, err := s.w.ExecContext(ctx,
		`UPDATE gateway_revocations SET expires_at = ?
		  WHERE session_id = ? AND expires_at < ?`,
		formatTime(expiresAt), sessionID, formatTime(expiresAt))
	if err != nil {
		return fmt.Errorf("store: extend gateway revocation: %w", err)
	}
	return nil
}

// SweepGatewayRevocations deletes the rows whose last affected token has
// expired and reports how many went. It runs from the control plane's periodic
// sweep; a deployment that never calls it still behaves correctly, it just
// keeps rows longer than a token can live.
func (s *Store) SweepGatewayRevocations(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM gateway_revocations WHERE expires_at <= ?`, formatTime(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("store: sweep gateway revocations: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// modernc's driver reports it; if it ever does not, the sweep still ran
		// and "unknown" is not worth failing the loop over.
		return 0, nil
	}
	return n, nil
}

// GatewayRevocationCount is how many revocations are on record. Diagnostics: the
// boot log prints it so an operator can see that revocation state survives a
// restart instead of having to trust a code comment.
func (s *Store) GatewayRevocationCount(ctx context.Context) (int, error) {
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_revocations`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count gateway revocations: %w", err)
	}
	return n, nil
}

// gatewayGenerationTx reads the generation inside an open transaction, so the
// revoke's read and write cannot interleave with another one.
func gatewayGenerationTx(ctx context.Context, tx *sql.Tx, sessionID string) (int64, error) {
	var gen int64
	err := tx.QueryRowContext(ctx,
		`SELECT generation FROM gateway_revocations WHERE session_id = ?`, sessionID).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read gateway revocation: %w", err)
	}
	return gen, nil
}
