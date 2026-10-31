package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// snapshotRowCount counts the rows for one sandbox directly.
//
// The point of the fix is the table, not the read API: GET .../snapshots is
// scoped by a live session, so it cannot show a row whose owner is gone. A count
// from the write connection is the only assertion that says "the row is gone".
func snapshotRowCount(t *testing.T, s *Store, sandboxID string) int {
	t.Helper()
	var n int
	if err := s.w.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM snapshots WHERE sandbox_id = ?`, sandboxID).Scan(&n); err != nil {
		t.Fatalf("count snapshots for %s: %v", sandboxID, err)
	}
	return n
}

// snapshotTotal counts every snapshot row, whatever its sandbox.
func snapshotTotal(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.w.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	return n
}

func mkSandbox(t *testing.T, s *Store, userID, projectID, rootDir string) *contracts.Sandbox {
	t.Helper()
	sb, err := s.CreateSandbox(context.Background(), userID, NewSandbox{
		ProjectID: projectID, State: contracts.SandboxReady, RootDir: rootDir,
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	return sb
}

// A snapshot row is owned by its sandbox row. Deleting the project cascades to
// sandboxes, so the rows have to go with it — before this, every reclaim left one
// row behind forever and nothing ever removed it.
func TestDeleteProjectRemovesItsSnapshotRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "snap@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "snap")
	sb := mkSandbox(t, s, u.ID, p.ID, "/tmp/snap")

	if _, serr := s.CreateSnapshot(ctx, u.ID, Snapshot{
		SandboxID: sb.ID, ContentHash: "h",
		BlobKey: sb.ID + "/20260101T000000.000000000Z/manifest.json",
	}); serr != nil {
		t.Fatalf("CreateSnapshot: %v", serr)
	}
	if got := snapshotRowCount(t, s, sb.ID); got != 1 {
		t.Fatalf("rows before delete = %d, want 1", got)
	}

	if err := s.DeleteProject(ctx, u.ID, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if got := snapshotRowCount(t, s, sb.ID); got != 0 {
		t.Fatalf("snapshot rows survived their project: %d (the directory lifecycle is closed, the rows were not)", got)
	}
}

// The ownership is a real foreign key, not cleanup code that a future delete path
// has to remember: deleting the sandboxes row itself takes the rows with it.
func TestSnapshotsCascadeWhenTheirSandboxRowGoes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "cascade@example.com", contracts.RoleUser)
	p := mkProject(t, s, u.ID, "cascade")
	sb := mkSandbox(t, s, u.ID, p.ID, "/tmp/cascade")
	if _, err := s.CreateSnapshot(ctx, u.ID, Snapshot{SandboxID: sb.ID, ContentHash: "h"}); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	rows, err := s.r.Query(`PRAGMA foreign_key_list(snapshots)`)
	if err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	defer rows.Close()
	cascades := false
	for rows.Next() {
		var (
			id, seq                                    int
			table, from, to, onUpdate, onDelete, match string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list: %v", err)
		}
		if table == "sandboxes" && from == "sandbox_id" && onDelete == "CASCADE" {
			cascades = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	if !cascades {
		t.Fatalf("snapshots has no ON DELETE CASCADE foreign key to sandboxes; the rows have no lifecycle")
	}

	if _, err := s.w.ExecContext(ctx, `DELETE FROM sandboxes WHERE id = ?`, sb.ID); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	if got := snapshotTotal(t, s); got != 0 {
		t.Fatalf("snapshot rows outlived their sandbox row: %d", got)
	}
}

// A deployment that already has snapshot rows gets the foreign key too. The
// migration is a rebuild, so it must be careful: every row is carried over
// (including one whose sandbox is already gone — that one is the janitor's to
// remove, not the migration's), and no rows are re-created behind the user's back.
func TestMigrationGivesExistingSnapshotRowsTheirOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	dsn, _, err := buildDSN(path)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	legacy, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	// The shape of the database before this fix: snapshots.sandbox_id is a plain
	// TEXT column with no foreign key, and one of the rows already points at a
	// sandbox that no longer exists.
	for _, stmt := range []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY, email TEXT NOT NULL COLLATE NOCASE UNIQUE,
			display_name TEXT NOT NULL DEFAULT '', password_hash TEXT NOT NULL DEFAULT '',
			idp_subject TEXT UNIQUE, role TEXT NOT NULL DEFAULT 'user',
			status TEXT NOT NULL DEFAULT 'active', settings TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL, deleted_at TEXT)`,
		`CREATE TABLE projects (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL, repo_url TEXT NOT NULL DEFAULT '',
			default_branch TEXT NOT NULL DEFAULT 'main', created_at TEXT NOT NULL,
			UNIQUE (user_id, name))`,
		`CREATE TABLE sandboxes (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			state TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '', node TEXT NOT NULL DEFAULT '',
			image TEXT NOT NULL DEFAULT '', root_dir TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '', cpu_milli INTEGER NOT NULL DEFAULT 0,
			mem_mb INTEGER NOT NULL DEFAULT 0, disk_gb INTEGER NOT NULL DEFAULT 0,
			last_active_at TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT,
			UNIQUE (project_id))`,
		`CREATE TABLE snapshots (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			sandbox_id TEXT NOT NULL, kind TEXT NOT NULL, git_commit TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0,
			blob_key TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL)`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("legacy schema: %v", err)
		}
	}
	now := formatTime(time.Now())
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`, []any{"usr_legacy", "legacy@example.com", now}},
		{`INSERT INTO projects (id, user_id, name, created_at) VALUES (?, ?, ?, ?)`, []any{"prj_legacy", "usr_legacy", "legacy", now}},
		{`INSERT INTO sandboxes (id, user_id, project_id, state, last_active_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{"sbx_legacy", "usr_legacy", "prj_legacy", "ready", now, now}},
		{`INSERT INTO snapshots (id, user_id, sandbox_id, kind, content_hash, blob_key, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"snp_live", "usr_legacy", "sbx_legacy", "fs", "h1", "sbx_legacy/20260101T000000.000000000Z/manifest.json", now}},
		{`INSERT INTO snapshots (id, user_id, sandbox_id, kind, content_hash, blob_key, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"snp_orphan", "usr_legacy", "sbx_gone", "fs", "h2", "sbx_gone/20260101T000000.000000000Z/manifest.json", now}},
	} {
		if _, err := legacy.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("legacy row: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	// Opening is the migration: the deployment's next start must not need a manual
	// step, and it must not lose the rows it already has.
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := snapshotTotal(t, s); got != 2 {
		t.Fatalf("migration changed the row count: %d, want 2 (every existing row is carried over)", got)
	}
	rows, err := s.r.Query(`PRAGMA foreign_key_list(snapshots)`)
	if err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	fk := false
	for rows.Next() {
		var (
			id, seq                                    int
			table, from, to, onUpdate, onDelete, match string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if table == "sandboxes" && from == "sandbox_id" && onDelete == "CASCADE" {
			fk = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_list: %v", err)
	}
	rows.Close()
	if !fk {
		t.Fatal("the migrated database still has no foreign key on snapshots.sandbox_id")
	}

	// The migrated table behaves like a fresh one: the live sandbox's row goes with
	// the project, the orphan (sandbox already gone) stays for the janitor.
	if err := s.DeleteProject(context.Background(), "usr_legacy", "prj_legacy"); err != nil {
		t.Fatalf("DeleteProject after migration: %v", err)
	}
	if got := snapshotRowCount(t, s, "sbx_legacy"); got != 0 {
		t.Fatalf("the live sandbox's row survived the delete: %d", got)
	}
	if got := snapshotRowCount(t, s, "sbx_gone"); got != 1 {
		t.Fatalf("the pre-existing orphan row was removed by the migration: %d (the janitor owns that decision)", got)
	}

	// Idempotent: a second start must not rebuild again or change anything.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if got := snapshotTotal(t, again); got != 1 {
		t.Fatalf("second migration changed the row count: %d, want 1", got)
	}
}
