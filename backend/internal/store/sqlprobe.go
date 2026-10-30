package store

// Per-connection activity recording, used to explain a stale read.
//
// `database/sql` never tells a driver which of its pooled connections an
// operation used, and `*sql.Conn` has no id. So the only place where a
// connection can be given an identity and a history is the driver boundary:
// this file wraps modernc.org/sqlite's driver with a decorator that records, per
// driver connection, the store call that is running on it, the SQL prefix, when
// it started and how it ended.
//
// It exists because of one symptom (docs/STATUS.md, "a pooled read connection
// served a stale snapshot"): under a specific load a read connection keeps
// serving a frozen SQLite snapshot, and nothing on the Go side of a `SELECT` can
// say which connection did that or what it had been doing before. With the
// wrapper, a stale read can name the connection, the store method that last ran
// on it, whether it was inside a transaction, whether a result set had been
// abandoned, and how long it had been idle.
//
// Cost: one `runtime.Callers` plus a few frame lookups per statement (a few
// microseconds), one mutex-guarded struct update, and one map entry per
// connection (the pools total five). PIFORGE_SQL_PROBE=0 turns it off.
//
// What it does NOT do: guess. It records what happened; the probe in
// stale_read_probe.go asks the connection itself (autocommit state,
// data_version, the row under dispute) and logs the answer, so "the connection
// was inside a transaction" is a measured statement, not an inference.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
)

// probeEnabled reports whether per-connection recording is on. It is on by
// default: the diagnostic is what makes a stale read explainable, and the cost
// is a few microseconds per statement.
var probeEnabled = os.Getenv("PIFORGE_SQL_PROBE") != "0"

// probeHistory is how many operations per connection are remembered. Eight is
// enough to see the shape of what a connection was doing before a stale read
// (the interrupted stream, the list query, the auth lookup) and small enough to
// stay a fixed allocation.
const probeHistory = 8

// storePkg is the prefix of the frames that are worth naming. Anything deeper is
// database/sql and the driver, which say nothing about intent.
const storePkg = "github.com/piforge/piforge/backend/internal/store."

var probeConnIDs atomic.Uint64

// probeRegistry holds every live driver connection by id. A connection is added
// when it is opened and deleted when it is closed, so this stays at the pool
// size (five) rather than growing with uptime.
var probeRegistry sync.Map // uint64 -> *probeConn

// probeOp is one operation on one connection.
type probeOp struct {
	kind   string // "query" or "exec"
	tag    string // the store method that issued it, e.g. "(*Store).ListProjects"
	sql    string // normalized statement, for a readable log line
	start  time.Time
	took   time.Duration
	err    string
	rows   int
	closed bool // the result set was closed without being read: an aborted read
	seq    int64
}

// probeConn is the driver.Conn decorator. database/sql guarantees that one
// connection is used by one goroutine at a time, so the mutex here is only for
// the snapshot a diagnostic path takes.
type probeConn struct {
	raw  driver.Conn
	id   uint64
	pool string

	openedAt time.Time
	opener   string // the store function that caused the connection to be dialled
	// poison is set when a statement on this connection was interrupted, and read
	// (and cleared) by ResetSession, which then tells database/sql to throw the
	// connection away instead of handing it to the next request.
	poison atomic.Bool

	mu      sync.Mutex
	state   probeConnState
	history []probeOp // oldest first, capped at probeHistory
}

type probeConnState struct {
	ops         int64
	tag         string
	sql         string
	startedAt   time.Time
	endedAt     time.Time
	inFlight    int
	txDepth     int
	openRows    int
	lastErr     string
	lastErrTag  string
	lastErrAt   time.Time
	resets      int64
	abandoned   int64 // result sets closed before being read: an interrupted read
	lastAbandon time.Time
	// interrupted counts statements that ended because their context was
	// cancelled. Such a connection is discarded rather than reused: a statement
	// interrupted mid-flight is what leaves a modernc connection serving a
	// snapshot frozen at the moment of the interrupt (see stale_read_probe.go and
	// docs/STATUS.md).
	interrupted int64
}

// probeRows decorates a result set so the connection can tell whether the reader
// asked for any of it. An abandoned result set is what a client that disconnects
// mid-stream leaves behind.
type probeRows struct {
	driver.Rows
	c       *probeConn
	op      probeOp
	rows    int
	stepped bool
	closed  bool
}

func (r *probeRows) Next(dest []driver.Value) error {
	r.stepped = true
	err := r.Rows.Next(dest)
	if err == nil {
		r.rows++
		return nil
	}
	r.finish()
	if err != io.EOF && interrupted(err) {
		// An interrupted read is the shape that freezes a connection; poison it so
		// ResetSession retires it instead of handing it to the next request.
		r.c.mu.Lock()
		r.c.state.interrupted++
		r.c.mu.Unlock()
		r.c.poison.Store(true)
	}
	return err
}

func (r *probeRows) Close() error {
	err := r.Rows.Close()
	r.finish()
	return err
}

func (r *probeRows) finish() {
	r.c.finishRows(r)
}

// probeTx decorates a transaction so the connection knows whether it is inside
// one. That was one of the ruled-out hypotheses; recording it per connection is
// how the next stale read can rule it out (or in) without a new experiment.
type probeTx struct {
	driver.Tx
	c    *probeConn
	done atomic.Bool
}

func (t *probeTx) Commit() error {
	err := t.Tx.Commit()
	t.finish(err)
	return err
}

func (t *probeTx) Rollback() error {
	err := t.Tx.Rollback()
	t.finish(err)
	return err
}

func (t *probeTx) finish(err error) {
	if t.done.Swap(true) {
		return
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.c.state.txDepth > 0 {
		t.c.state.txDepth--
	}
	if err != nil {
		t.c.state.lastErr = err.Error()
	}
}

// ---------------------------------------------------------------------------
// Connector
// ---------------------------------------------------------------------------

// probeConnector dials modernc's sqlite driver and wraps what it returns. It is
// used with sql.OpenDB so the package does not have to register a second driver
// name (which panics when two tests register the same name).
type probeConnector struct {
	dsn  string
	pool string
}

func (c probeConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return newProbeConn(raw, c.pool), nil
}

func (c probeConnector) Driver() driver.Driver { return &sqlite.Driver{} }

// openProbePool opens one pool whose connections are all instrumented.
func openProbePool(dsn, pool string) *sql.DB {
	return sql.OpenDB(probeConnector{dsn: dsn, pool: pool})
}

func newProbeConn(raw driver.Conn, pool string) *probeConn {
	c := &probeConn{
		raw:      raw,
		id:       probeConnIDs.Add(1),
		pool:     pool,
		openedAt: time.Now(),
		opener:   callerTag(),
	}
	if probeEnabled {
		probeRegistry.Store(c.id, c)
	}
	return c
}

// ---------------------------------------------------------------------------
// driver.Conn and friends
// ---------------------------------------------------------------------------

func (c *probeConn) Prepare(query string) (driver.Stmt, error) { return c.raw.Prepare(query) }

func (c *probeConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	p, ok := c.raw.(driver.ConnPrepareContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return p.PrepareContext(ctx, query)
}

func (c *probeConn) Close() error {
	probeRegistry.Delete(c.id)
	return c.raw.Close()
}

func (c *probeConn) Begin() (driver.Tx, error) {
	tx, err := c.raw.Begin()
	if err != nil {
		return nil, err
	}
	return c.wrapTx(tx), nil
}

func (c *probeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.raw.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return c.wrapTx(tx), nil
}

func (c *probeConn) wrapTx(tx driver.Tx) driver.Tx {
	c.mu.Lock()
	c.state.txDepth++
	c.mu.Unlock()
	return &probeTx{Tx: tx, c: c}
}

func (c *probeConn) Ping(ctx context.Context) error {
	if p, ok := c.raw.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *probeConn) ResetSession(ctx context.Context) error {
	// The poison check is unconditional: it is a correctness fix, not a
	// diagnostic, so PIFORGE_SQL_PROBE=0 must not turn it off.
	if c.poison.Swap(false) {
		// A statement on this connection was interrupted. modernc leaves such a
		// connection serving the snapshot it held when the interrupt landed --
		// autocommit, no open rows, no transaction, and still blind to commits that
		// happen afterwards (measured: see stale_read_probe.go). The store cannot
		// repair that state from SQL, so the connection is handed back as dead;
		// database/sql closes it and dials a fresh one.
		noteRetirement(c)
		return driver.ErrBadConn
	}
	if probeEnabled {
		c.mu.Lock()
		c.state.resets++
		c.mu.Unlock()
	}
	if r, ok := c.raw.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

// interrupted reports whether a completed operation ended because its context
// was cancelled. It is the one signal the store has for "this connection may now
// be serving a frozen snapshot".
func interrupted(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "interrupted") || strings.Contains(msg, "context canceled")
}

// retirementCount and lastRetirementLog bound how often a retirement is logged:
// an abort storm retires connections continuously, and one line per retirement
// would be a log flood instead of evidence. One line a second, with a running
// total, is enough to see that the policy is what changed.
var (
	retirementCount    atomic.Int64
	lastRetirementLog  atomic.Int64 // unix nano
	retiredConnCounter atomic.Int64
)

func noteRetirement(c *probeConn) {
	n := retirementCount.Add(1)
	now := time.Now()
	last := lastRetirementLog.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < time.Second {
		return
	}
	if !lastRetirementLog.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	snap := c.snapshot(now)
	log.Printf("store: retired an interrupted connection %s (total retired %d, this one: %s)",
		fmt.Sprintf("%s#%d", snap.Pool, snap.ID), n, snap.describeConn())
}

// Retirements reports how many connections were retired after an interrupted
// statement. Tests and the diagnostic use it.
func (s *Store) Retirements() int64 { return retirementCount.Load() }

func (c *probeConn) IsValid() bool {
	if v, ok := c.raw.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// Raw exists so `sql.Conn.Raw` can hand the diagnostic the concrete connection.
// modernc's conn does not implement driver.RawConn, so without this the store
// could not look at the connection a stale read came from. It deliberately
// exposes the wrapper, not the driver's own conn: nothing outside this file may
// touch the underlying connection while database/sql believes it is idle.
func (c *probeConn) Raw(f func(driverConn any) error) error { return f(c) }

func (c *probeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.raw.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	op := c.beginOp("exec", query)
	res, err := e.ExecContext(ctx, query, args)
	c.endOp(op, err)
	return res, err
}

func (c *probeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.raw.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	op := c.beginOp("query", query)
	rows, err := q.QueryContext(ctx, query, args)
	return c.wrapRows(op, rows, err), err
}

// ---------------------------------------------------------------------------
// Recording
// ---------------------------------------------------------------------------

func (c *probeConn) beginOp(kind, query string) probeOp {
	now := time.Now()
	c.mu.Lock()
	c.state.ops++
	op := probeOp{
		kind:  kind,
		tag:   callerTag(),
		sql:   normalizeSQL(query),
		start: now,
		seq:   c.state.ops,
	}
	c.state.tag = op.tag
	c.state.sql = op.sql
	c.state.startedAt = now
	c.state.inFlight++
	c.mu.Unlock()
	return op
}

// endOp finishes an operation that returns no result set: the connection is idle
// again as soon as it returns.
func (c *probeConn) endOp(op probeOp, err error) {
	now := time.Now()
	if err != nil {
		op.err = err.Error()
	}
	op.took = now.Sub(op.start)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordLocked(op)
	if c.state.inFlight > 0 {
		c.state.inFlight--
	}
	c.state.endedAt = now
	if op.err != "" {
		c.state.lastErr = op.err
		c.state.lastErrTag = op.tag
		c.state.lastErrAt = now
	}
	if interrupted(err) {
		c.state.interrupted++
		c.poison.Store(true)
	}
}

// recordLocked appends the operation to the per-connection history, keeping the
// ring at probeHistory entries.
func (c *probeConn) recordLocked(op probeOp) {
	if len(c.history) >= probeHistory {
		copy(c.history, c.history[1:])
		c.history = c.history[:probeHistory-1]
	}
	c.history = append(c.history, op)
}

// wrapRows keeps the connection checked out until the result set is closed,
// which is what "in use by a reader" means for `database/sql`. inFlight stays at
// one between here and finishRows.
func (c *probeConn) wrapRows(op probeOp, rows driver.Rows, err error) driver.Rows {
	if err != nil || rows == nil {
		c.endOp(op, err)
		return nil
	}
	op.took = time.Since(op.start)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordLocked(op)
	c.state.openRows++
	return &probeRows{Rows: rows, c: c, op: op}
}

func (c *probeConn) finishRows(r *probeRows) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	if c.state.openRows > 0 {
		c.state.openRows--
	}
	if c.state.inFlight > 0 {
		c.state.inFlight--
	}
	c.state.endedAt = time.Now()
	for i := len(c.history) - 1; i >= 0; i-- {
		if c.history[i].seq == r.op.seq {
			c.history[i].rows = r.rows
			if !r.stepped {
				c.history[i].closed = true
			}
			break
		}
	}
	if !r.stepped {
		c.state.abandoned++
		c.state.lastAbandon = time.Now()
	}
}

// ---------------------------------------------------------------------------
// Snapshot and naming
// ---------------------------------------------------------------------------

// connSnapshot is what the store can say about one pooled connection.
type connSnapshot struct {
	ID        uint64
	Pool      string
	Tag       string
	SQL       string
	InFlight  int
	TxDepth   int
	OpenRows  int
	Ops       int64
	Resets    int64
	Abandoned int64
	IdleFor   time.Duration
	BusyFor   time.Duration
	OpenedAt  time.Time
	Opener    string
	LastErr    string
	LastErrTag string
	LastErrAt  time.Time
	History    []probeOp
}

func (c *probeConn) snapshot(now time.Time) connSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := connSnapshot{
		ID:        c.id,
		Pool:      c.pool,
		Tag:       c.state.tag,
		SQL:       c.state.sql,
		InFlight:  c.state.inFlight,
		TxDepth:   c.state.txDepth,
		OpenRows:  c.state.openRows,
		Ops:       c.state.ops,
		Resets:    c.state.resets,
		Abandoned: c.state.abandoned,
		OpenedAt:  c.openedAt,
		Opener:    c.opener,
		LastErr:    c.state.lastErr,
		LastErrTag: c.state.lastErrTag,
		LastErrAt:  c.state.lastErrAt,
		History:   append([]probeOp(nil), c.history...),
	}
	if !c.state.endedAt.IsZero() {
		s.IdleFor = now.Sub(c.state.endedAt)
	}
	if !c.state.startedAt.IsZero() {
		s.BusyFor = now.Sub(c.state.startedAt)
	}
	return s
}

// snapshotOf takes a snapshot from the value `sql.Conn.Raw` hands out. The
// second result is false when the connection is not one of ours.
func snapshotOf(driverConn any) (connSnapshot, bool) {
	c, ok := driverConn.(*probeConn)
	if !ok {
		return connSnapshot{}, false
	}
	return c.snapshot(time.Now()), true
}

// callerTag names the store method on the stack. It filters the frame list down
// to this package and skips the recording plumbing, so the name is the call the
// user's request made (ListProjects, AuthSessionUser, SessionEvents, ...).
func callerTag() string {
	if !probeEnabled {
		return ""
	}
	var pcs [24]uintptr
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		name := frame.Function
		if strings.HasPrefix(name, storePkg) {
			short := strings.TrimPrefix(name, storePkg)
			if !strings.HasPrefix(short, "(*probe") && !strings.HasPrefix(short, "probe") &&
				short != "callerTag" && !strings.HasPrefix(short, "openProbePool") &&
				!strings.HasPrefix(short, "newProbeConn") {
				return short
			}
		}
		if !more {
			return ""
		}
	}
}

// normalizeSQL keeps the shape of a statement (verb + table or just the verb) so
// a log line can be read at a glance and aggregated into a histogram.
func normalizeSQL(query string) string {
	q := strings.Join(strings.Fields(query), " ")
	if len(q) > 96 {
		q = q[:96] + "…"
	}
	return q
}

// describeConn renders a snapshot as log line fields.
func (s connSnapshot) describeConn() string {
	return fmt.Sprintf("%s#%d tag=%q sql=%q in_flight=%d tx_depth=%d open_rows=%d ops=%d resets=%d abandoned=%d idle=%s busy=%s last_err=%q last_err_on=%q last_err_at=%s opened_by=%q",
		s.Pool, s.ID, s.Tag, s.SQL, s.InFlight, s.TxDepth, s.OpenRows, s.Ops, s.Resets,
		s.Abandoned, s.IdleFor.Round(time.Millisecond), s.BusyFor.Round(time.Millisecond),
		trim(s.LastErr, 120), s.LastErrTag, s.LastErrAt.UTC().Format(time.RFC3339Nano), s.Opener)
}

func (s connSnapshot) describeHistory() string {
	var parts []string
	for _, op := range s.History {
		part := fmt.Sprintf("%s[%s %s took=%s", op.kind, op.tag, op.sql, op.took.Round(time.Millisecond))
		if op.err != "" {
			part += " err=" + trim(op.err, 60)
		}
		if op.rows > 0 {
			part += fmt.Sprintf(" rows=%d", op.rows)
		}
		if op.closed {
			part += " closed_unread"
		}
		parts = append(parts, part+"]")
	}
	return strings.Join(parts, " ")
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
