package store

// What a stale read records, and why.
//
// `AuthSessionUser` is the one read whose "not found" must never be wrong: it is
// what turns a live cookie into a 401. When that lookup misses on the read pool
// and the writer still has the row, the read pool is stale (see
// docs/STATUS.md, "a read connection served a stale snapshot"). Until now the
// store could only count that and drop the pool; it could not say which
// connection was at fault or what that connection had been doing.
//
// This file is the second half: with sqlprobe.go recording every statement on
// every connection, a detection now names the connection, the store call that
// last ran on it, whether it was inside a transaction, whether a result set had
// been abandoned unread, and how long it had been idle. It then asks that same
// connection three questions, because those answers are facts:
//
//   - does it still fail to see the row (so the fault is the connection, not a
//     transient interleaving)?
//   - is it in autocommit mode, i.e. is there an open transaction on it? A
//     `BEGIN` that succeeds proves autocommit; one that fails with "cannot start
//     a transaction within a transaction" proves the opposite.
//   - does it see the same projects rows the writer sees? That is the "wrong
//     data beyond the auth lookup" question, answered per event instead of by
//     argument.
//
// The probe is rate limited and only runs on a miss, so a request with a forged
// or expired cookie cannot be used to make the server run it in a loop.
//
// # What the measurement says (docs/STATUS.md, "读连接旧快照")
//
// It is not a transaction and not an unclosed result set: on every detection the
// connection is in autocommit (`BEGIN` succeeds on it), no rows are open, and
// the abandoned-result-set count is zero. It is not the writer either: the writer
// reads the row the reader misses, and the reader misses it even without the
// expiry filter.
//
// What every detection had in common was last_err="context canceled": the
// connection's last statement was interrupted because the client went away, and
// the connection then answers from the snapshot it held at that moment --
// autocommit, empty, and still blind to commits that landed afterwards. That is
// driver-level pager state, so the store cannot repair it with SQL. The fix
// lives in sqlprobe.go: a connection whose statement was interrupted is retired
// on its next checkout (ResetSession -> driver.ErrBadConn) instead of being
// handed to another request, and this detector stays as the second line of
// defence.
//
// The risk of that policy: every aborted request costs a connection. On a local
// file database that is microseconds; on a remote or encrypted volume it is not
// free, and an abort storm pays it continuously. It is paid deliberately: a
// connection that answers from an old snapshot is the one failure this store
// must not hand to a user.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// staleReadReport is what one detection measured. It is kept (the last one) so a
// test can assert the diagnostic actually recorded something, rather than
// trusting a log line nobody reads.
type staleReadReport struct {
	At       time.Time
	Conn     connSnapshot
	Probed   bool
	TxnState string // "autocommit", "in_transaction", or "unknown: <why>"
	// The row under dispute, read again on the same connection.
	SameConnSeesRow bool
	// The token row without the expiry filter: tells "row missing" apart from
	// "the filter rejected the row".
	SameConnSeesToken bool
	// PRAGMA data_version on each side. SQLite bumps it when another connection
	// commits, so an equal pair means the read connection is looking at the same
	// database state the writer last wrote.
	ReadDataVersion  int64
	WriteDataVersion int64
	// Row counts per table, read on each side: what the connection can see
	// beyond the auth lookup.
	ReadTables  string
	WriteTables string
	WriteSeesRow    bool
	WriterAvailable bool
}

// probeMinInterval bounds how often the expensive probe runs. A miss on a token
// that was never valid (a stale cookie after logout) would otherwise probe on
// every request.
const probeMinInterval = 2 * time.Second

// probeStaleConn describes the connection a missed auth read came from.
//
// The caller still holds the connection; this runs on it and releases nothing.
// Every statement here uses a detached context with its own deadline: if the
// probe used the request's context, a client disconnecting between BEGIN and
// ROLLBACK would leave a transaction open on the connection -- the probe would
// then be manufacturing the very condition it is measuring.
func (s *Store) probeStaleConn(ctx context.Context, conn *sql.Conn, tokenHash string, now time.Time) *staleReadReport {
	rep := &staleReadReport{At: time.Now()}
	if probeEnabled {
		if err := conn.Raw(func(driverConn any) error {
			if snap, ok := snapshotOf(driverConn); ok {
				rep.Conn = snap
			}
			return nil
		}); err != nil {
			rep.TxnState = "unknown: raw: " + err.Error()
		}
	}

	last := s.lastProbeAt.Load()
	if last != 0 && time.Since(time.Unix(0, last)) < probeMinInterval {
		return rep
	}
	s.lastProbeAt.Store(time.Now().UnixNano())
	rep.Probed = true

	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	// 1. The same connection, asked again. A frozen snapshot answers the same
	// way every time; a transient interleaving does not.
	var found int
	err := conn.QueryRowContext(probeCtx,
		`SELECT COUNT(*) FROM auth_sessions a JOIN users u ON u.id = a.user_id
		  WHERE a.token_hash = ? AND a.expires_at > ? AND u.deleted_at IS NULL`,
		tokenHash, formatTime(now)).Scan(&found)
	switch {
	case err != nil:
		rep.TxnState = "unknown: recheck: " + err.Error()
	case found > 0:
		rep.SameConnSeesRow = true
	}

	// 2. The row without the expiry filter, so "the row is missing" and "the
	// filter rejected it" cannot be confused.
	if err := conn.QueryRowContext(probeCtx,
		`SELECT COUNT(*) FROM auth_sessions WHERE token_hash = ?`, tokenHash).Scan(&found); err == nil {
		rep.SameConnSeesToken = found > 0
	}

	// 3. data_version: the connection's own view of how many writes it has seen.
	var dv int64
	if err := conn.QueryRowContext(probeCtx, "PRAGMA data_version").Scan(&dv); err == nil {
		rep.ReadDataVersion = dv
	}

	// 4. Other tables, so "only auth is affected" is measured, not assumed. The
	// counts matter: the fingerprint below is a count plus a timestamp, and the
	// count is what moves when another connection commits.
	rep.ReadTables = tableFingerprint(probeCtx, conn)

	// 5. Autocommit. `BEGIN` succeeds only outside a transaction; inside one
	// SQLite answers "cannot start a transaction within a transaction". This is
	// the direct answer to "was the frozen connection stuck in a read
	// transaction?", the hypothesis a store-level experiment could not settle.
	tx, terr := conn.ExecContext(probeCtx, "BEGIN")
	switch {
	case terr == nil:
		rep.TxnState = "autocommit"
		_ = tx
		if _, rerr := conn.ExecContext(probeCtx, "ROLLBACK"); rerr != nil {
			// The connection now has an open transaction we cannot close, so it is
			// poisoned: say so, and let the caller drop the pool (it does either
			// way). Reporting it is the honest alternative to hiding it.
			rep.TxnState = "autocommit (rollback failed: " + rerr.Error() + ")"
		}
	case strings.Contains(strings.ToLower(terr.Error()), "within a transaction"):
		rep.TxnState = "in_transaction"
	default:
		rep.TxnState = "unknown: " + terr.Error()
	}

	return rep
}

// tableFingerprint reports what the connection can see in the tables the load
// touches. auth_sessions is the table under dispute; projects and session_events
// are the "beyond auth" check.
func tableFingerprint(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) string {
	var sessions, events, projects int
	var newest sql.NullString
	_ = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_sessions`).Scan(&sessions)
	_ = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_events`).Scan(&events)
	_ = q.QueryRowContext(ctx, `SELECT COUNT(*), MAX(created_at) FROM projects`).Scan(&projects, &newest)
	return fmt.Sprintf("auth_sessions:%d session_events:%d projects:%d/%s", sessions, events, projects, newest.String)
}

// noteStaleRead records a confirmed stale read (the writer had the row) and logs
// what the diagnostic measured.
func (s *Store) noteStaleRead(rep *staleReadReport) {
	if rep != nil {
		s.lastStale.Store(rep)
	}
	n := s.staleReads.Add(1)
	if rep == nil {
		log.Printf("store: stale read #%d: no diagnostic (the read pool could not be interrogated)", n)
		return
	}
	if rep.Conn.ID == 0 {
		log.Printf("store: stale read #%d detected: the connection could not be named (no probe identity); probe=%t txn_state=%q",
			n, rep.Probed, rep.TxnState)
	} else {
		log.Printf("store: stale read #%d detected: %s", n, rep.Conn.describeConn())
	}
	if len(rep.Conn.History) > 0 {
		log.Printf("store: stale read #%d history: %s", n, rep.Conn.describeHistory())
	}
	if rep.Probed {
		log.Printf("store: stale read #%d probe: same_conn_sees_row=%t same_conn_sees_token=%t txn_state=%q data_version=read:%d write:%d read[%s] write[%s] write_sees_row=%t",
			n, rep.SameConnSeesRow, rep.SameConnSeesToken, rep.TxnState, rep.ReadDataVersion,
			rep.WriteDataVersion, rep.ReadTables, rep.WriteTables, rep.WriteSeesRow)
	} else {
		log.Printf("store: stale read #%d probe: skipped (rate limited)", n)
	}
	log.Printf("store: stale read #%d: dropping the idle read pool", n)
}

// LastStaleRead returns the most recent detection, or nil. Tests use it to assert
// that the diagnostic recorded an identity and a tag, not just a counter.
func (s *Store) LastStaleRead() *staleReadReport {
	return s.lastStale.Load()
}

// writerFacts asks the writer the same questions the read connection was asked,
// so the log can compare them.
func (s *Store) writerFacts(ctx context.Context, tokenHash string, now time.Time) (dataVersion int64, tables string, seesRow bool) {
	var found int
	if err := s.w.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_sessions a JOIN users u ON u.id = a.user_id
		  WHERE a.token_hash = ? AND a.expires_at > ? AND u.deleted_at IS NULL`,
		tokenHash, formatTime(now)).Scan(&found); err == nil && found > 0 {
		seesRow = true
	}
	if err := s.w.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataVersion); err != nil {
		dataVersion = -1
	}
	tables = tableFingerprint(ctx, s.w)
	return dataVersion, tables, seesRow
}


