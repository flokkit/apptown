// Package query executes checked statements against a pool or transaction.
package query

import (
	"context"
	"database/sql"
)

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Handle struct {
	Executor
	Check func(string) error
	Error func(error, string) error
}

func (d Handle) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := d.Check(q); err != nil {
		return nil, d.Error(err, q)
	}
	r, err := d.Executor.ExecContext(ctx, q, args...)
	return r, d.Error(err, q)
}

func (d Handle) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := d.Check(q); err != nil {
		return nil, d.Error(err, q)
	}
	r, err := d.Executor.QueryContext(ctx, q, args...)
	return r, d.Error(err, q)
}

func (d Handle) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if d.Check(q) != nil {
		return d.Executor.QueryRowContext(ctx, "SELECT forge_injected_failure()")
	}
	return d.Executor.QueryRowContext(ctx, q, args...)
}
