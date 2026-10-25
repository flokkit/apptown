// Package database owns PostgreSQL handles and checked query/transaction mechanics.
package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
)

// A failure belongs to one pool and its transactions, never to the process.
// Tests arm the next matching statement; production leaves the slot empty.
// Compare-and-swap makes "next" mean exactly once even for concurrent callers.
type statementFault struct{ next atomic.Pointer[pendingFault] }

// pendingFault fails the first statement containing fragment after skip
// earlier matching statements have run.
type pendingFault struct {
	fragment string
	skip     atomic.Int64
	// more is how many further matching statements fail after the first.
	more atomic.Int64
}

var ErrInjectedFailure = fmt.Errorf("injected statement failure")

func (f *statementFault) check(query string) error {
	if f != nil {
		if next := f.next.Load(); next != nil && next.fragment != "" && strings.Contains(query, next.fragment) && next.skip.Add(-1) < 0 {
			if next.more.Add(-1) >= 0 || f.next.CompareAndSwap(next, nil) {
				return ErrInjectedFailure
			}
		}
	}
	return nil
}

// DB owns the PostgreSQL connection pool and rebinds ? placeholders to $n.
// Concurrency is controlled by transactions, row locks and conditional writes.
// Close result sets before issuing another query to avoid exhausting the pool.
// Values are never rewritten: binary content belongs in bytea, not TEXT.
type DB struct {
	raw    *sql.DB
	faults statementFault
}

type Tx struct {
	tx     *sql.Tx
	faults *statementFault
}

// OpenRaw is a plain connection without schema changes, for readiness waits.
func OpenRaw(url string) (*sql.DB, error) { return openRaw(url, nil) }

// openRaw is OpenRaw, making each connection with dial when it is given.
func openRaw(url string, dial pgconn.DialFunc) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		dial = cfg.DialFunc
	}
	cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &cancellable{Conn: conn}, nil
	}
	cfg.BuildContextWatcherHandler = markCancels
	// Moments come back in UTC: AppTown
	// has only ever recorded and shown UTC, and a plain string scan of a
	// timestamptz must read "…Z", not the box's zone.
	sdb := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		// database/sql array values can pass through pgx's decode/re-encode
		// path. Preserve JSON numbers there, including int64 generations.
		jsonb := &pgtype.Type{Name: "jsonb", OID: pgtype.JSONBOID, Codec: &pgtype.JSONBCodec{
			Marshal: json.Marshal, Unmarshal: func(data []byte, dst any) error {
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.UseNumber()
				return decoder.Decode(dst)
			},
		}}
		conn.TypeMap().RegisterType(jsonb)
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "_jsonb", OID: pgtype.JSONBArrayOID, Codec: &pgtype.ArrayCodec{ElementType: jsonb}})
		return nil
	}), stdlib.OptionShouldPing(pingAfterCancel))
	sdb.SetMaxOpenConns(10)
	return sdb, nil
}

// A connection whose context was cancelled is pinged before it is used
// again, and dropped if the ping fails. pgx (v5.10, v5.11) can leave such a
// connection looking healthy with an error waiting in it: when a write is
// slow (over 15 ms, as on a loaded machine) pgx starts a background read,
// which can take the whole reply and wait on for more. A cancellation then
// sets a deadline on the connection, the waiting read stops with "i/o
// timeout", and that error is kept for whatever reads next: the next
// query, which fails although nothing cancelled it. The ping reads it
// instead, so the pool closes that connection and opens another.
const cancelledKey = "apptown.cancelled"

// cancelMarker is pgx's default handling of a cancelled context (a deadline
// on the connection that interrupts its read), which also marks the
// connection for a ping before its next use, and tells the connection
// which cancellation set its deadline.
type cancelMarker struct {
	*pgconn.DeadlineContextWatcherHandler
	cancelled *atomic.Bool
	conn      *cancellable
}

func (h cancelMarker) HandleCancel(ctx context.Context) {
	h.cancelled.Store(true)
	if h.conn != nil {
		cause := ctx.Err()
		h.conn.cause.Store(&cause)
	}
	h.DeadlineContextWatcherHandler.HandleCancel(ctx)
}

func (h cancelMarker) HandleUnwatchAfterCancel() {
	h.DeadlineContextWatcherHandler.HandleUnwatchAfterCancel()
	if h.conn != nil {
		h.conn.cause.Store(nil)
	}
}

func markCancels(pc *pgconn.PgConn) ctxwatch.Handler {
	cancelled := new(atomic.Bool)
	pc.CustomData()[cancelledKey] = cancelled
	return cancelMarker{&pgconn.DeadlineContextWatcherHandler{Conn: pc.Conn()}, cancelled, beneath(pc.Conn())}
}

// cancellable is a connection that knows when its deadline is a caller's
// cancellation. pgx stops a cancelled statement by setting a deadline on
// the connection, and turns the timeout that follows into the context's
// error when it reads, but not when it writes: a statement whose write the
// cancellation cut short failed with "write failed: ... i/o timeout", which
// no caller can tell from a network fault. Here a read or write that times
// out while a cancellation's deadline is set fails with an error that is
// both the context's error and the network's.
type cancellable struct {
	net.Conn
	cause atomic.Pointer[error]
}

func (c *cancellable) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.explain(err)
}

func (c *cancellable) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.explain(err)
}

func (c *cancellable) explain(err error) error {
	var ne net.Error
	if err == nil || !errors.As(err, &ne) || !ne.Timeout() {
		return err
	}
	if cause := c.cause.Load(); cause != nil && *cause != nil {
		return &cancelledIO{cause: *cause, err: err, net: ne}
	}
	return err
}

// cancelledIO is a read or write a cancellation's deadline stopped: it is
// the cancellation (errors.Is context.Canceled or DeadlineExceeded) and
// still the network's timeout, a net.Error as before (pgx and TLS look for
// one).
type cancelledIO struct {
	cause, err error
	net        net.Error // err, or what it wraps
}

func (e *cancelledIO) Error() string   { return e.cause.Error() + ": " + e.err.Error() }
func (e *cancelledIO) Unwrap() []error { return []error{e.cause, e.err} }
func (e *cancelledIO) Timeout() bool   { return e.net.Timeout() }

// Temporary: a cancelled statement is not one to try again.
func (e *cancelledIO) Temporary() bool { return false }

// beneath finds the cancellable under pgx's connection, which may be TLS
// over it.
func beneath(conn net.Conn) *cancellable {
	for {
		switch c := conn.(type) {
		case *cancellable:
			return c
		case interface{ NetConn() net.Conn }:
			conn = c.NetConn()
		default:
			return nil
		}
	}
}

// pingAfterCancel pings a connection that was cancelled since its last use,
// and one idle for over a second as pgx does by default.
func pingAfterCancel(_ context.Context, p stdlib.ShouldPingParams) bool {
	cancelled, _ := p.Conn.PgConn().CustomData()[cancelledKey].(*atomic.Bool)
	return cancelled != nil && cancelled.Swap(false) || p.IdleDuration > time.Second
}

func (d *DB) Close() error { return d.raw.Close() }
func (d *DB) Ping() error  { return d.raw.Ping() }

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	if err := d.faults.check(query); err != nil {
		return nil, QueryError(err, query)
	}
	res, err := d.raw.Exec(Rebind(query), args...)
	return res, QueryError(err, query)
}

func (d *DB) Query(query string, args ...any) (*Rows, error) {
	if err := d.faults.check(query); err != nil {
		return nil, QueryError(err, query)
	}
	rows, err := d.raw.Query(Rebind(query), args...)
	if err != nil {
		return nil, QueryError(err, query)
	}
	return &Rows{Rows: rows, q: query}, nil
}

func (d *DB) QueryRow(query string, args ...any) *Row {
	if err := d.faults.check(query); err != nil {
		return &Row{row: d.raw.QueryRow("SELECT forge_injected_failure()"), q: query}
	}
	return &Row{row: d.raw.QueryRow(Rebind(query), args...), q: query}
}

func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	tx, err := d.raw.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx, faults: &d.faults}, nil
}

// ReadSnapshot keeps related read-only facts at one database revision.
func (d *DB) ReadSnapshot(ctx context.Context) (*Tx, error) {
	tx, err := d.raw.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	snapshot := &Tx{tx: tx, faults: &d.faults}
	if _, err := snapshot.SQL().ExecContext(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		_ = snapshot.Rollback() // unchecked-error: setup already failed; rollback cleanup does not establish a successful snapshot.
		return nil, err
	}
	return snapshot, nil
}

func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	if err := t.faults.check(query); err != nil {
		return nil, QueryError(err, query)
	}
	res, err := t.tx.Exec(Rebind(query), args...)
	return res, QueryError(err, query)
}

func (t *Tx) Query(query string, args ...any) (*Rows, error) {
	if err := t.faults.check(query); err != nil {
		return nil, QueryError(err, query)
	}
	rows, err := t.tx.Query(Rebind(query), args...)
	if err != nil {
		return nil, QueryError(err, query)
	}
	return &Rows{Rows: rows, q: query}, nil
}

func (t *Tx) QueryRow(query string, args ...any) *Row {
	if err := t.faults.check(query); err != nil {
		// A row that fails at Scan: run a statement that cannot succeed.
		return &Row{row: t.tx.QueryRow("SELECT forge_injected_failure()"), q: query}
	}
	return &Row{row: t.tx.QueryRow(Rebind(query), args...), q: query}
}

func (t *Tx) Commit() error   { return t.tx.Commit() }
func (t *Tx) Rollback() error { return t.tx.Rollback() }

// Errors that mean something. A failure
// carries the first 80 characters of its statement, attached once, at the
// handle; no row is not a failure and passes through bare so callers can
// ask errors.Is(err, sql.ErrNoRows) or, better, Lookup.

// Row is a single-row result whose errors name their statement.
type Row struct {
	row *sql.Row
	q   string
}

func (r *Row) Scan(dst ...any) error { return QueryError(r.row.Scan(dst...), r.q) }
func (r *Row) Err() error            { return QueryError(r.row.Err(), r.q) }

// Rows is a result set whose errors name their statement. Close reports
// an iteration error the caller never asked about, so a loop that ended
// early is at least loud.
type Rows struct {
	*sql.Rows
	q       string
	checked bool
}

func (r *Rows) Err() error {
	r.checked = true
	return QueryError(r.Rows.Err(), r.q)
}

func (r *Rows) Close() error {
	err := r.Rows.Close()
	if !r.checked {
		if e := r.Rows.Err(); e != nil {
			log.Printf("query ended with an error nobody checked: %v — %s", e, stmt(r.q))
		}
	}
	return err
}

func QueryError(err error, q string) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return fmt.Errorf("%w — %s", err, stmt(q))
}

func stmt(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	return strs.Truncate(q, 80)
}

// Lookup sorts a single-row read's outcome: found, absent, or failed.
//
//	found, err := Lookup(a.DB.QueryRow(q, id).Scan(&x))
func Lookup(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	}
	return false, err
}

// Missing classifies a single-row read's absence: sql.ErrNoRows becomes a
// fault.NotFound that still matches sql.ErrNoRows, and any other error or nil
// passes through. Use it where an owner reads a record by its identity, never
// after a write: a write that returns no row was not recorded, which is a
// failure, not an absence.
func Missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fault.Wrap(fault.NotFound, err)
	}
	return err
}

// --- placeholder rebinding --------------------------------------------------

// Rebind turns `?` placeholders into `$1`, `$2`, …, skipping single-quoted
// strings (” escapes included), `--` line comments and dollar-quoted
// strings. This adapter serves the legacy DB/Tx methods. Native PostgreSQL
// queries use SQL() directly, including queries with JSON ? operators.
func Rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			j := i + 1
			for j < len(query) {
				if query[j] == '\'' {
					if j+1 < len(query) && query[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(query) {
				j = len(query) - 1
			}
			b.WriteString(query[i : j+1])
			i = j
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				b.WriteString(query[i:])
				i = len(query)
			} else {
				b.WriteString(query[i : i+j])
				i = i + j - 1
			}
		case c == '$' && i+1 < len(query) && query[i+1] == '$':
			j := strings.Index(query[i+2:], "$$")
			if j < 0 {
				b.WriteString(query[i:])
				i = len(query)
			} else {
				b.WriteString(query[i : i+2+j+2])
				i = i + 2 + j + 1
			}
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// InTx owns transaction cleanup. The operation commits on success and rolls
// back on an error or panic; domain guards and receipts stay in the callback.

func (d *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // unchecked-error: callback/commit supplies the outcome; deferred rollback also runs after a completed transaction.
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Transact publishes a result only after its writes commit. An error never
// returns an uncommitted receipt that a caller could accidentally dispatch.
func Transact[T any](ctx context.Context, db *DB, fn func(*Tx) (T, error)) (T, error) {
	var result T
	err := db.InTx(ctx, func(tx *Tx) error {
		var err error
		result, err = fn(tx)
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

// Wrap adopts an existing pool. It does not migrate, seed or open another pool.
func Wrap(pool *sql.DB) *DB { return &DB{raw: pool} }

// Open opens and checks a pool without applying application schema or data.
func Open(url string) (*DB, error) { return OpenWith(url, nil) }

// OpenWith is Open with every connection made by dial (nil is the default
// dialer). The test database harness gives its own, which closes
// connections without leaving them waiting in TIME_WAIT.
func OpenWith(url string, dial pgconn.DialFunc) (*DB, error) {
	pool, err := openRaw(url, dial)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(); err != nil {
		_ = pool.Close() // unchecked-error: ping failure is returned; closing the unusable pool cannot make opening succeed.
		return nil, fmt.Errorf("database: %w", err)
	}
	return Wrap(pool), nil
}

// Pool is for infrastructure adapters such as River and schema installation.
// Domain queries use SQL so they retain the checked query boundary.
func (d *DB) Pool() *sql.DB   { return d.raw }
func (t *Tx) Native() *sql.Tx { return t.tx }

// FailNextStatement scopes a one-shot fault to this pool and its transactions.
// The returned cleanup only clears this installation, never a newer one.
func (d *DB) FailNextStatement(fragment string) func() {
	return d.FailStatementAfter(fragment, 0)
}

// FailStatements fails the next n statements containing fragment: a fault
// that outlasts a caller's retries.
func (d *DB) FailStatements(fragment string, n int) func() {
	fault := &pendingFault{fragment: fragment}
	fault.more.Store(int64(n - 1))
	d.faults.next.Store(fault)
	return func() { d.faults.next.CompareAndSwap(fault, nil) }
}

// FailStatementAfter lets skip matching statements run, then fails the next
// one. Use it when an earlier read of the same query is not the one a test
// is about.
func (d *DB) FailStatementAfter(fragment string, skip int) func() {
	fault := &pendingFault{fragment: fragment}
	fault.skip.Store(int64(skip))
	d.faults.next.Store(fault)
	return func() { d.faults.next.CompareAndSwap(fault, nil) }
}
