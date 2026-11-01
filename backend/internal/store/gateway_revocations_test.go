package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// A gateway token revocation has to survive the one event that used to erase it:
// a control-plane restart. These tests therefore always close the store and open
// the same file again before re-reading — "same process, same map" would pass
// against the in-memory set this table replaced and prove nothing.
func TestGatewayRevocationSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "piforge.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Nothing revoked yet: generation 0, and that is not an error.
	if gen, err := st.GatewayRevocationGeneration(ctx, "ses_restart"); err != nil || gen != 0 {
		t.Fatalf("fresh session generation = %d, %v; want 0, nil", gen, err)
	}
	first, err := st.RevokeGatewayToken(ctx, "ses_restart", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("RevokeGatewayToken: %v", err)
	}
	if first != 1 {
		t.Fatalf("first generation = %d, want 1", first)
	}
	// A second stop moves it to 2, and a different session stays at 0.
	second, err := st.RevokeGatewayToken(ctx, "ses_restart", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("RevokeGatewayToken (second): %v", err)
	}
	if second != 2 {
		t.Fatalf("second generation = %d, want 2", second)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The restart.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gen, err := reopened.GatewayRevocationGeneration(ctx, "ses_restart")
	if err != nil {
		t.Fatalf("generation after restart: %v", err)
	}
	if gen != 2 {
		t.Fatalf("generation after restart = %d, want 2 (a restart must not forget a revocation)", gen)
	}
	rev, ok, err := reopened.GatewayRevocation(ctx, "ses_restart")
	if err != nil || !ok {
		t.Fatalf("GatewayRevocation after restart = %+v, ok=%v, err=%v", rev, ok, err)
	}
	if rev.Generation != 2 || rev.ExpiresAt.IsZero() || rev.RevokedAt.IsZero() {
		t.Fatalf("persisted row = %+v; want generation 2 and both timestamps", rev)
	}
	if n, err := reopened.GatewayRevocationCount(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d, %v; want 1, nil", n, err)
	}
}

// The sweep is what keeps a tombstone from becoming permanent: once the last
// token a revocation could have affected has expired, the row goes. The test
// also pins the other half — a row that still has a live token is not touched.
func TestGatewayRevocationSweepRemovesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now()

	expired := "ses_expired"
	live := "ses_live"
	if _, err := st.RevokeGatewayToken(ctx, expired, now.Add(-time.Minute)); err != nil {
		t.Fatalf("revoke %s: %v", expired, err)
	}
	if _, err := st.RevokeGatewayToken(ctx, live, now.Add(time.Hour)); err != nil {
		t.Fatalf("revoke %s: %v", live, err)
	}

	// A past expiry is deliberately clamped to the revocation instant ("a
	// revocation is valid at least from now", RevokeGatewayToken), so the sweep
	// has to run at an instant *after* the revoke call. Sweeping at the same
	// `now` this test started with was a race the Windows development host hid:
	// its clock is coarse enough that `now` and the store's `now` were the same
	// value, while Linux's nanosecond clock makes the store's clamp a microsecond
	// later and the sweep deletes nothing — the suite was deterministically red
	// on a native Linux host (docs/LINUX-VERIFY.md, row L13).
	sweepAt := time.Now().Add(time.Second)

	deleted, err := st.SweepGatewayRevocations(ctx, sweepAt)
	if err != nil {
		t.Fatalf("SweepGatewayRevocations: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("swept %d rows, want 1", deleted)
	}
	if gen, err := st.GatewayRevocationGeneration(ctx, expired); err != nil || gen != 0 {
		t.Fatalf("expired row still reads generation %d, %v; want 0", gen, err)
	}
	if gen, err := st.GatewayRevocationGeneration(ctx, live); err != nil || gen != 1 {
		t.Fatalf("live row reads generation %d, %v; want 1", gen, err)
	}

	// Sweeping again is a no-op, not an error or a double delete.
	if deleted, err := st.SweepGatewayRevocations(ctx, sweepAt); err != nil || deleted != 0 {
		t.Fatalf("second sweep = %d, %v; want 0, nil", deleted, err)
	}
}

// ExtendGatewayRevocation is the resumed-session case: the revoke row's expiry
// was "the last token minted before the stop", but a token minted *after* it (a
// resume) lives longer, and the sweep must not delete the generation that token
// carries. A session with no row is a no-op.
func TestGatewayRevocationExtendKeepsResumedTokenAlive(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now()

	const session = "ses_resumed"
	if _, err := st.RevokeGatewayToken(ctx, session, now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	later := now.Add(2 * time.Hour)
	if err := st.ExtendGatewayRevocation(ctx, session, later); err != nil {
		t.Fatalf("ExtendGatewayRevocation: %v", err)
	}
	// The moment the old deadline passed, the row is still there because a
	// resumed token still needs its generation.
	if deleted, err := st.SweepGatewayRevocations(ctx, now.Add(2*time.Minute)); err != nil || deleted != 0 {
		t.Fatalf("sweep before the extended deadline = %d, %v; want 0, nil", deleted, err)
	}
	if gen, err := st.GatewayRevocationGeneration(ctx, session); err != nil || gen != 1 {
		t.Fatalf("generation after extend = %d, %v; want 1", gen, err)
	}
	// Extending never shortens: an earlier deadline is ignored.
	if err := st.ExtendGatewayRevocation(ctx, session, now); err != nil {
		t.Fatalf("ExtendGatewayRevocation (backwards): %v", err)
	}
	if deleted, err := st.SweepGatewayRevocations(ctx, now.Add(2*time.Minute)); err != nil || deleted != 0 {
		t.Fatalf("a backwards extend shortened the row: %d, %v", deleted, err)
	}
	// And the row is eventually swept.
	if deleted, err := st.SweepGatewayRevocations(ctx, later.Add(time.Second)); err != nil || deleted != 1 {
		t.Fatalf("sweep after the extended deadline = %d, %v; want 1, nil", deleted, err)
	}
}

// The row must outlive the session row it refers to. A deleted session's tokens
// (minted at generation 0, before the revoke) must stay dead; a foreign key with
// ON DELETE CASCADE would remove the tombstone and resurrect them.
func TestGatewayRevocationsHaveNoCascadingForeignKey(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rows, err := st.r.QueryContext(ctx, `PRAGMA foreign_key_list(gateway_revocations)`)
	if err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("gateway_revocations has a foreign key; a cascading delete would resurrect a revoked session's tokens")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
}

// A deleted session leaves its tombstone in place: the row is not deleted by the
// session's delete, and the generation is still what a stale token has to match.
func TestGatewayRevocationOutlivesTheSession(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	user := mkUser(t, st, "revoke@piforge.local", contracts.RoleUser)
	project := mkProject(t, st, user.ID, "revoke")
	session := mkSession(t, st, user.ID, project.ID, contracts.KindMain)

	if _, err := st.RevokeGatewayToken(ctx, session.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := st.DeleteSession(ctx, user.ID, session.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if gen, err := st.GatewayRevocationGeneration(ctx, session.ID); err != nil || gen != 1 {
		t.Fatalf("generation after the session was deleted = %d, %v; want 1", gen, err)
	}
}
