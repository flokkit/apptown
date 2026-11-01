package workflow

import (
	"context"
	"database/sql"

	"github.com/flokkit/apptown/internal/database/query"
	"github.com/flokkit/apptown/internal/dbtest"
)

// Count real database round trips at the same interface generated code uses.
type countedQueries struct {
	query.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() query.Handle {
	return dbtest.WithExecutor(d.Handle, d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.Executor.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.Executor.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.Executor.QueryRowContext(ctx, q, args...)
}
