package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAuthSessionSurvivesAStaleReadPool pins the behaviour behind "signed out
// after a while": a read connection that serves a snapshot older than the
// writer's must not turn a live session into a 401.
//
// The stale pool is built honestly: the database is checkpointed, copied, and
// the copy is used as the read pool. It therefore sees everything committed
// before the copy -- the account included -- and nothing committed after it,
// which is exactly what a frozen read snapshot does to the real server.
func TestAuthSessionSurvivesAStaleReadPool(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := mkUser(t, s, "stale@piforge.local", "user")

	if _, err := s.w.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	copyFile(t, s.path, snapshot)
	dsn, _, err := buildDSN(snapshot)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	stale, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open the snapshot: %v", err)
	}
	original := s.r
	s.r = stale
	// The real read pool is no longer reachable through the store; close it now
	// so it cannot keep the database file open past the test.
	if original != stale {
		if err := original.Close(); err != nil {
			t.Fatalf("close the original read pool: %v", err)
		}
	}

	token := "hash-stale-read"
	if err := s.CreateAuthSession(ctx, user.ID, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAuthSession: %v", err)
	}

	// The reader cannot see the session; the writer can. The answer must be the
	// user, not "no session".
	got, err := s.AuthSessionUser(ctx, token, time.Now())
	if err != nil {
		t.Fatalf("a stale read pool lost a live session: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("user = %s, want %s", got.ID, user.ID)
	}
	if s.staleReads.Load() == 0 {
		t.Fatal("the store did not notice that its read pool was stale")
	}

	// A token that truly does not exist is still "no session": the fallback
	// must not turn authentication into "anything goes".
	if _, err := s.AuthSessionUser(ctx, "hash-does-not-exist", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: err = %v, want ErrNotFound", err)
	}

	// The snapshot DB lives in the test's temp dir; release it before the
	// cleanup tries to remove the directory (Windows keeps the file locked
	// otherwise). The store's own Close is idempotent.
	if err := stale.Close(); err != nil {
		t.Fatalf("close the snapshot pool: %v", err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatalf("open %s: %v", from, err)
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatalf("create %s: %v", to, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy %s: %v", from, err)
	}
}
