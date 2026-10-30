package store

import (
	"context"
	"testing"
	"time"
)

// TestAnInterruptedStatementRetiresItsConnection pins the store-layer half of the
// stale-read fix.
//
// Measured on the deployed server (docs/STATUS.md): a read connection whose
// statement was interrupted answers later queries from the snapshot it held when
// the interrupt landed -- autocommit, no open rows, no transaction, still blind
// to commits that come after. The store cannot fix that state from SQL, so the
// contract is: a connection that had a statement interrupted is never handed to
// another request. database/sql replaces it on the next checkout, which is what
// this test observes.
func TestAnInterruptedStatementRetiresItsConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "retire@piforge.local", "user")

	// One pinned connection, used for a query that is cancelled before it runs.
	first, err := s.r.Conn(ctx)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	var firstID uint64
	if err := first.Raw(func(driverConn any) error {
		snap, ok := snapshotOf(driverConn)
		if !ok {
			t.Fatal("the read pool is not instrumented")
		}
		firstID = snap.ID
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := first.QueryContext(cancelled, `SELECT COUNT(*) FROM users`); err == nil {
		t.Fatal("the cancelled query succeeded, so nothing was interrupted")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// The writer commits a row after the interruption. Only a fresh connection is
	// promised to see it, and that is the point: the frozen one is gone.
	if err := s.ExecForTest(ctx, `INSERT INTO projects (id, user_id, name, repo_url, default_branch, created_at)
		VALUES ('prj_retire', ?, 'after', '', 'main', ?)`, user.ID, formatTime(s.now())); err != nil {
		t.Fatalf("insert: %v", err)
	}

	second, err := s.r.Conn(ctx)
	if err != nil {
		t.Fatalf("pin again: %v", err)
	}
	defer second.Close()
	var secondID uint64
	if err := second.Raw(func(driverConn any) error {
		snap, ok := snapshotOf(driverConn)
		if ok {
			secondID = snap.ID
		}
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if secondID == 0 {
		t.Fatal("the replacement connection is not instrumented")
	}
	if secondID == firstID {
		t.Fatalf("connection #%d survived an interrupted statement; it must be retired", firstID)
	}

	var seen int
	if err := second.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = 'prj_retire'`).Scan(&seen); err != nil {
		t.Fatalf("read after the interruption: %v", err)
	}
	if seen != 1 {
		t.Fatal("the replacement connection cannot see the row committed after the interruption")
	}
}

// TestAHealthyConnectionIsReused is the other half of the contract: retiring
// interrupted connections must not turn into "a new connection per query".
func TestAHealthyConnectionIsReused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CountUsers(ctx); err != nil {
		t.Fatalf("CountUsers: %v", err)
	}

	connID := func() uint64 {
		t.Helper()
		conn, err := s.r.Conn(ctx)
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		defer conn.Close()
		var id uint64
		if err := conn.Raw(func(driverConn any) error {
			snap, _ := snapshotOf(driverConn)
			id = snap.ID
			return nil
		}); err != nil {
			t.Fatalf("raw: %v", err)
		}
		return id
	}
	first := connID()
	time.Sleep(time.Millisecond)
	if second := connID(); second != first {
		t.Fatalf("a healthy connection was replaced: #%d then #%d", first, second)
	}
}
