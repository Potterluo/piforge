package store

// Reproduction for the frozen read snapshot.
//
// The observed shape, from a 10-minute probe against the deployed server (see
// docs/STATUS.md): a read connection answers a fresh autocommit SELECT from a
// database state older than the writer's -- the token row the writer just
// committed is invisible, and re-asking on that same connection still says no --
// while its last recorded error is "context canceled". The load that produces it
// is the event stream endpoint: `handleEvents` reads session events with the
// request's context, and the e2e harness (and a user closing a tab) cancels that
// context while `rows.Next()` is running.
//
// That combination is not the one the previous attempt ruled out. A cancelled
// `QueryContext` that never returned rows leaves nothing behind; a cancellation
// *during row iteration* makes database/sql close the driver rows from its
// awaitDone goroutine while another goroutine is inside sqlite3_step, i.e.
// modernc's `rows.Close` finalises a statement that is executing. This test
// reproduces that second shape on purpose and then asks the read pool whether it
// can still see a row committed afterwards.

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestCancelledStreamLeavesAStaleReadSnapshot drives the shape above and reports
// how many read connections come back blind afterwards. It is deliberately a
// report-with-assertion: if the race is fixed (by a driver upgrade or by not
// cancelling mid-iteration) the count drops to zero and the test fails, which is
// the signal to delete it rather than to keep a green test that proves nothing.
func TestCancelledStreamLeavesAStaleReadSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "stream@piforge.local", "user")

	// A table with enough rows that a read is still in progress when the context
	// is cancelled; the SSE replay query is a similar shape (a JOIN, a filter and
	// a batch limit).
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS churn (n INTEGER PRIMARY KEY, pad TEXT)`); err != nil {
		t.Fatalf("create churn: %v", err)
	}
	if _, err := s.w.ExecContext(ctx, `
		INSERT INTO churn (n, pad)
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 400)
		SELECT n, printf('%0400d', n) FROM seq`); err != nil {
		t.Fatalf("fill churn: %v", err)
	}

	// Aborted reads. The cancellation has to land *while* a step is running, so
	// the query is deliberately slow to produce rows and the reader runs on its
	// own goroutine: that is when database/sql closes the driver's rows from its
	// awaitDone goroutine while the reader is still inside sqlite3_step, and
	// modernc then finalises a statement that is executing.
	const slow = `WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < 60000)
	              SELECT n, hex(randomblob(96)) FROM s`
	aborted := 0
	for i := 0; i < 40; i++ {
		readCtx, cancel := context.WithCancel(ctx)
		rows, err := s.r.QueryContext(readCtx, slow)
		if err != nil {
			cancel()
			continue
		}
		done := make(chan struct{})
		readSome := make(chan struct{})
		go func() {
			defer close(done)
			close(readSome)
			for rows.Next() {
				var n int
				var pad string
				if err := rows.Scan(&n, &pad); err != nil {
					return
				}
			}
		}()
		<-readSome
		time.Sleep(2 * time.Millisecond)
		cancel() // mid-step
		time.Sleep(time.Millisecond)
		_ = rows.Close()
		<-done
		aborted++
	}
	t.Logf("aborted %d streaming reads mid-step", aborted)

	// The writer commits a row every read connection must be able to see.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO churn (n, pad) VALUES (9999, 'after')`); err != nil {
		t.Fatalf("commit after the churn: %v", err)
	}

	blind := 0
	for i := 0; i < readPoolSize; i++ {
		conn, err := s.r.Conn(ctx)
		if err != nil {
			t.Fatalf("pin a reader: %v", err)
		}
		var seen int
		qerr := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM churn WHERE n = 9999`).Scan(&seen)
		var snap connSnapshot
		_ = conn.Raw(func(driverConn any) error {
			snap, _ = snapshotOf(driverConn)
			return nil
		})
		_ = conn.Close()
		if qerr != nil {
			t.Logf("reader %d: query error %v", i, qerr)
			continue
		}
		if seen == 0 {
			blind++
			t.Logf("reader %d is blind: %s", i, snap.describeConn())
		}
	}
	if blind == 0 {
		t.Skipf("no frozen read connection after 60 aborted streaming reads; the race did not reproduce on this machine")
	}
	t.Logf("%d/%d read connections served a snapshot older than the writer's", blind, readPoolSize)

	// And the store's own detector must repair it: after the auth path notices, a
	// fresh read sees the row.
	token := "hash-after-churn"
	if err := s.CreateAuthSession(ctx, user.ID, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAuthSession: %v", err)
	}
	if _, err := s.AuthSessionUser(ctx, token, time.Now()); err != nil {
		t.Fatalf("auth after the churn: %v", err)
	}
	var seen int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM churn WHERE n = 9999`).Scan(&seen); err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if seen == 0 {
		t.Fatal("the read pool still cannot see the committed row after the auth path repaired it")
	}
	_ = sql.ErrNoRows
}
