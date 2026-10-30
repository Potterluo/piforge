// Package store is the SQLite persistence layer for PiForge.
//
// # Isolation model
//
// The product has exactly two roles and no row-level security (PRD §8.1), so
// isolation is enforced in the application layer instead:
//
//   - every repository method takes the caller's userID;
//   - every statement that touches a user-owned table carries a
//     "user_id = ?" (or an EXISTS subquery on the owning row) predicate;
//   - nothing falls back to "all users" — a missing predicate is a bug, not a
//     feature.
//
// There is exactly one deliberate exception: the AdminXxx method set, which
// first proves the caller holds role='admin' and is only reachable from the
// admin-only HTTP routes. Shared (admin-scope) providers are readable through
// InheritedProviders because the product wants users to see them (PRD §5.7.1
// "Inherited" badge); that call still validates the caller.
//
// The auth primitives (UserByEmail, AuthSessionUser, ...) are the other
// unavoidable pre-identity lookups: they run before a user is known, and each
// one resolves at most one row by a unique key.
//
// # Concurrency
//
// SQLite allows a single writer. The write pool is therefore capped at one
// connection (see Open), which removes SQLITE_BUSY handling from the call
// sites and makes the per-session event sequence allocation atomic. Reads use
// a separate pool so that SSE replay and list queries do not queue behind
// writes.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

//go:embed schema.sql
var schemaFS embed.FS

// Sentinel errors. Callers classify failures with errors.Is; the HTTP layer
// turns ErrNotFound into 404 (never 403, to avoid resource probing) and
// ErrConflict into 409.
var (
	// ErrNotFound means the row does not exist *for this user*. It is also
	// what a cross-user access returns, deliberately.
	ErrNotFound = errors.New("not found")
	// ErrForbidden means the caller is authentic but not allowed (admin-only
	// paths).
	ErrForbidden = errors.New("forbidden")
	// ErrConflict means a uniqueness or state conflict (duplicate name,
	// already archived, ...).
	ErrConflict = errors.New("conflict")
	// ErrInvalid means the caller passed something the store refuses.
	ErrInvalid = errors.New("invalid argument")
)

// timeLayout is how every timestamp is persisted. UTC and nanoseconds so that
// string ordering equals time ordering, which keeps cursor queries simple.
const timeLayout = time.RFC3339Nano

// Store is a handle to one database file. It is safe for concurrent use.
type Store struct {
	// r is the read pool. It may be the same handle as w for in-memory
	// databases.
	r *sql.DB
	// w is the single-connection write pool (see the package comment).
	w *sql.DB
	// now is injectable so tests can freeze time.
	now    func() time.Time
	path   string
	memory bool
	// staleReads counts how often a read connection was found to be serving a
	// snapshot older than the writer's. It should be zero; the auth path repairs
	// the pool when it happens (see AuthSessionUser) and this counter is what a
	// regression test asserts on.
	staleReads atomic.Int64
	// lastStale is the diagnostic for the most recent detection: which connection
	// served the stale read, what it had been doing, and what it answered when it
	// was asked directly (see stale_read_probe.go).
	lastStale atomic.Pointer[staleReadReport]
	// lastProbeAt is the unix-nano time of the last full probe; a token nobody
	// ever issued must not be able to drive the diagnosis in a loop.
	lastProbeAt atomic.Int64
}

// readPoolSize is how many connections the read pool keeps. Four is enough for
// the list/SSE endpoints to run beside each other; the number matters here
// because dropping a stale connection works by shrinking the idle pool and
// growing it back.
const readPoolSize = 4

// discardStaleReaders drops every idle read connection.
//
// A read connection that comes back from an interrupted query can still hold an
// open read transaction, and SQLite then serves that frozen snapshot to every
// later query on that connection -- which is how a long-lived server starts
// answering 401 (or "project not found") for rows that are committed. Closing
// the idle connections is the supported way to get rid of it without touching
// queries that are in flight. What the guilty connection had been doing is
// logged by noteStaleRead before this runs.
func (s *Store) discardStaleReaders() {
	s.r.SetMaxIdleConns(0)
	s.r.SetMaxIdleConns(readPoolSize)
}

// Open opens (creating if needed) the SQLite database at path, applies the
// embedded schema idempotently and returns a ready Store. An empty path or
// ":memory:" gives a throwaway in-memory database, which is only useful when
// both pools share one connection (they do: see OpenWithNow).
func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithNow(ctx, path, time.Now)
}

// OpenWithNow is Open with an injectable clock, used by tests that need
// deterministic timestamps.
func OpenWithNow(ctx context.Context, path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	dsn, memory, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	// Every pool is opened through the probe connector (sqlprobe.go) so a stale
	// read can name the connection it came from. sql.OpenDB does not dial.
	w := openProbePool(dsn, "write")
	// One writer. SQLite serialises writes anyway; doing it in the pool means
	// every statement either gets the write lock immediately or waits in Go
	// instead of failing with SQLITE_BUSY, and it makes the
	// "INSERT ... SELECT COALESCE(MAX(seq),0)+1" sequence allocation atomic
	// without a separate lock.
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)

	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: ping %q: %w", path, err)
	}
	if err := migrate(ctx, w); err != nil {
		_ = w.Close()
		return nil, err
	}

	s := &Store{w: w, r: w, now: now, path: path, memory: memory}

	if !memory {
		// Separate read pool: WAL lets readers run concurrently with the single
		// writer, so SSE replay and list endpoints never wait on an in-flight
		// write.
		r := openProbePool(dsn, "read")
		r.SetMaxOpenConns(readPoolSize)
		r.SetMaxIdleConns(readPoolSize)
		r.SetConnMaxLifetime(0)
		if err := r.PingContext(ctx); err != nil {
			_ = r.Close()
			_ = w.Close()
			return nil, fmt.Errorf("store: ping read pool %q: %w", path, err)
		}
		s.r = r
	}
	return s, nil
}

// buildDSN turns a filesystem path into a modernc.org/sqlite DSN with the
// pragmas the rest of the package assumes: WAL for concurrent readers, a
// busy timeout for the rare moment when a checkpoint holds the write lock, and
// foreign keys on so that ON DELETE CASCADE actually fires.
func buildDSN(path string) (dsn string, memory bool, err error) {
	const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	if path == "" || path == ":memory:" {
		return "file::memory:?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", true, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("store: resolve %q: %w", path, err)
	}
	if dir := filepath.Dir(abs); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", false, fmt.Errorf("store: create directory for %q: %w", path, err)
		}
	}
	return "file:" + filepath.ToSlash(abs) + "?" + pragmas, false, nil
}

// migrate applies schema.sql. Every statement is idempotent, so this runs on
// every Open rather than behind a version table.
func migrate(ctx context.Context, db *sql.DB) error {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("store: read schema: %w", err)
	}
	if err := execScript(ctx, db, string(raw)); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// Columns added after the first release. `CREATE TABLE IF NOT EXISTS` cannot
	// add them to an existing database, and SQLite has no "add column if not
	// exists", so the duplicate-column error is the success case.
	if err := addColumn(ctx, db, "sessions", "ephemeral", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// Per-sandbox API keys (docs/SANDBOX.md). A row created before this column
	// existed reads back empty, which the control plane treats as "recreate it or
	// use the fixed override" rather than "call the sandbox unauthenticated".
	if err := addColumn(ctx, db, "sandboxes", "api_key_cipher", "BLOB"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// addColumn adds a column unless the table already has it.
func addColumn(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	_, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+ddl)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

// execScript runs a multi-statement SQL script by splitting on line-final
// semicolons. The schema is authored by us and deliberately keeps every
// statement on lines that end with ';' and never contains a semicolon inside a
// string literal, so this is exact — and it avoids depending on whether a
// driver executes multiple statements per Exec.
func execScript(ctx context.Context, db *sql.DB, script string) error {
	var b strings.Builder
	flush := func() error {
		stmt := strings.TrimSpace(b.String())
		b.Reset()
		if stmt == "" {
			return nil
		}
		// A chunk that is only comments has nothing to execute.
		if !strings.Contains(strings.ToUpper(stmt), "CREATE") &&
			!strings.Contains(strings.ToUpper(stmt), "INSERT") &&
			!strings.Contains(strings.ToUpper(stmt), "ALTER") {
			return nil
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w (statement: %.60s)", err, stmt)
		}
		return nil
	}
	for _, line := range strings.Split(script, "\n") {
		b.WriteString(line)
		b.WriteString("\n")
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// Close closes both pools. It is safe to call more than once.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.r != nil && s.r != s.w {
		if err := s.r.Close(); err != nil {
			first = err
		}
	}
	if s.w != nil {
		if err := s.w.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Ping reports whether the database is reachable; /readyz uses it.
func (s *Store) Ping(ctx context.Context) error { return s.r.PingContext(ctx) }

// Path returns the filesystem path the store was opened with ("" for memory).
func (s *Store) Path() string { return s.path }

// Now returns the store's clock, so callers stamp rows with the same time
// source the store uses.
func (s *Store) Now() time.Time { return s.now() }

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// isAdmin is the explicit admin path guard (PRD §8.1): every AdminXxx method
// starts here so an admin read can never happen by accident.
func (s *Store) isAdmin(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("store: admin check: %w", ErrForbidden)
	}
	var role string
	err := s.r.QueryRowContext(ctx,
		`SELECT role FROM users WHERE id = ? AND status = 'active' AND deleted_at IS NULL`,
		userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("store: admin check: %w", ErrForbidden)
	}
	if err != nil {
		return false, fmt.Errorf("store: admin check: %w", err)
	}
	return role == "admin", nil
}

// requireAdmin returns ErrForbidden unless the caller is an active admin.
func (s *Store) requireAdmin(ctx context.Context, userID string) error {
	ok, err := s.isAdmin(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("store: admin only: %w", ErrForbidden)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// FormatTimeForTest exposes the store's timestamp layout to tests that need to
// age a row (an expired grant is otherwise untestable without waiting).
func FormatTimeForTest(t time.Time) string { return formatTime(t) }

// ExecForTest runs a statement against the store, for tests that need to move a
// row into a state the API cannot produce on demand.
func (s *Store) ExecForTest(ctx context.Context, query string, args ...any) error {
	_, err := s.w.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: exec: %w", err)
	}
	return nil
}
