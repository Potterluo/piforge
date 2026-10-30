package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestStaleReadNamesTheConnectionAndItsWork pins the diagnostic itself: when a
// read connection serves a snapshot that is missing a row the writer has, the
// store must be able to say *which* connection it was, what store call ran on it
// last, and whether that connection is inside a transaction.
//
// The stale read is built the same way the real one appears: the read pool is a
// copy of the database taken before the row existed. The copy is opened through
// the probe connector, so the connection is instrumented exactly like a deployed
// server's read pool.
func TestStaleReadNamesTheConnectionAndItsWork(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "diagnose@piforge.local", "user")

	// Commit a checkpointed copy and point the read pool at it: everything
	// committed from now on is invisible to the readers.
	if _, err := s.w.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	copyFile(t, s.path, snapshot)
	dsn, _, err := buildDSN(snapshot)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	stale := openProbePool(dsn, "read")
	if err := stale.PingContext(ctx); err != nil {
		t.Fatalf("ping the snapshot pool: %v", err)
	}
	original := s.r
	s.r = stale
	if original != stale {
		if err := original.Close(); err != nil {
			t.Fatalf("close the original read pool: %v", err)
		}
	}
	t.Cleanup(func() { _ = stale.Close() })

	// Both rows are written after the copy, so the readers cannot see either.
	// The project goes in through the writer directly: CreateProject reads its own
	// row back through the (deliberately stale) read pool.
	token := "hash-diagnose"
	if err := s.CreateAuthSession(ctx, user.ID, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAuthSession: %v", err)
	}
	if err := s.ExecForTest(ctx,
		`INSERT INTO projects (id, user_id, name, repo_url, default_branch, created_at)
		 VALUES (?, ?, ?, '', 'main', ?)`,
		"prj_after_copy", user.ID, "after-the-copy", formatTime(s.now())); err != nil {
		t.Fatalf("insert a project: %v", err)
	}

	got, err := s.AuthSessionUser(ctx, token, time.Now())
	if err != nil {
		t.Fatalf("a stale read pool lost a live session: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("user = %s, want %s", got.ID, user.ID)
	}

	rep := s.LastStaleRead()
	if rep == nil {
		t.Fatal("the store detected a stale read but kept no diagnostic")
	}
	// The identity is the whole point: without it the log line is a symptom.
	if rep.Conn.ID == 0 {
		t.Fatalf("the stale connection was not identified: %+v", rep.Conn)
	}
	if rep.Conn.Pool != "read" {
		t.Fatalf("pool = %q, want read (the writer must not be blamed)", rep.Conn.Pool)
	}
	// The tag names the store call that ran on it, not database/sql plumbing.
	if rep.Conn.Tag != "(*Store).authSessionUserOn" {
		t.Fatalf("tag = %q, want the auth lookup that ran on the connection", rep.Conn.Tag)
	}
	if len(rep.Conn.History) == 0 {
		t.Fatal("no history was recorded for the stale connection")
	}
	// The measured facts about the connection itself.
	if !rep.Probed {
		t.Fatal("the diagnostic did not probe the connection")
	}
	if rep.TxnState != "autocommit" {
		t.Fatalf("txn state = %q, want autocommit: a frozen snapshot is not a stuck transaction", rep.TxnState)
	}
	if rep.SameConnSeesRow {
		t.Fatal("the same connection reported seeing the row it had just missed")
	}
	if !rep.WriteSeesRow {
		t.Fatal("the writer did not see the row that committed it")
	}
	// Another table, so "only auth is affected" is a measurement: the writer has
	// the project created after the copy, the frozen reader does not.
	if rep.ReadTables == rep.WriteTables {
		t.Fatalf("table fingerprints equal on both sides (%s): the copy should differ after the write", rep.ReadTables)
	}
	// The token row is gone from the stale connection's view entirely -- not just
	// filtered by the expiry comparison.
	if rep.SameConnSeesToken {
		t.Fatal("the stale connection saw the token row without the expiry filter but missed it with it")
	}
}

// TestProbeRecordsStatementTags checks the recorder itself: a query through the
// writer pool must be attributed to the store method that issued it, and a
// transaction must be visible while it is open.
func TestProbeRecordsStatementTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "tags@piforge.local", "user")

	if _, err := s.CountUsers(ctx); err != nil {
		t.Fatalf("CountUsers: %v", err)
	}

	conn, err := s.w.Conn(ctx)
	if err != nil {
		t.Fatalf("pin the writer: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var snap connSnapshot
	if err := conn.Raw(func(driverConn any) error {
		got, ok := snapshotOf(driverConn)
		if !ok {
			t.Fatal("the writer connection is not instrumented")
		}
		snap = got
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if snap.Pool != "write" {
		t.Fatalf("pool = %q, want write", snap.Pool)
	}
	if snap.InFlight != 0 || snap.OpenRows != 0 {
		t.Fatalf("idle connection reported in_flight=%d open_rows=%d", snap.InFlight, snap.OpenRows)
	}
	if len(snap.History) == 0 {
		t.Fatal("no statement history on the writer connection")
	}
	if snap.History[len(snap.History)-1].sql != "SELECT 1" {
		t.Fatalf("last statement = %q, want the one just executed", snap.History[len(snap.History)-1].sql)
	}

	// Inside a transaction the same connection must report it. The transaction is
	// taken on the pinned connection because the writer pool is one connection
	// deep: `s.w.Begin()` here would wait for this very connection forever.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var inTx connSnapshot
	if err := conn.Raw(func(driverConn any) error {
		inTx, _ = snapshotOf(driverConn)
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if inTx.TxDepth != 1 {
		t.Fatalf("tx depth = %d, want 1 while a transaction is open", inTx.TxDepth)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := conn.Raw(func(driverConn any) error {
		inTx, _ = snapshotOf(driverConn)
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if inTx.TxDepth != 0 {
		t.Fatalf("tx depth = %d after rollback, want 0", inTx.TxDepth)
	}

	// And the user row created before all this is visible to the writer.
	if _, err := s.UserByID(ctx, user.ID); err != nil {
		t.Fatalf("UserByID: %v", err)
	}
}
