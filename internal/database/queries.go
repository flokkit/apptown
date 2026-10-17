package database

import (
	"context"
	"database/sql"
)

// Generated queries use database/sql directly, on the same pool or transaction
// as handwritten queries. Keep failure injection on this boundary too, so
// migrating a write cannot silently bypass the atomicity tests.
type Queryer interface{ SQL() Handle }

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Handle struct {
	DBTX
	faults *statementFault
}

func (d *DB) SQL() Handle { return Handle{DBTX: d.raw, faults: &d.faults} }
func (t *Tx) SQL() Handle { return Handle{DBTX: t.tx, faults: t.faults} }

func (d Handle) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := d.faults.check(q); err != nil {
		return nil, QueryError(err, q)
	}
	r, err := d.DBTX.ExecContext(ctx, q, args...)
	return r, QueryError(err, q)
}

func (d Handle) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := d.faults.check(q); err != nil {
		return nil, QueryError(err, q)
	}
	r, err := d.DBTX.QueryContext(ctx, q, args...)
	return r, QueryError(err, q)
}

func (d Handle) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if d.faults.check(q) != nil {
		return d.DBTX.QueryRowContext(ctx, "SELECT forge_injected_failure()")
	}
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

// WithExecutor decorates statement execution while retaining this handle's fault
// scope. It is used by query-budget tests and other infrastructure instrumentation.
func (d Handle) WithExecutor(executor DBTX) Handle { d.DBTX = executor; return d }
