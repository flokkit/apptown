package workflow

import (
	"context"
	"database/sql"

	"flokkit.io/forge/internal/database"
)

// Count real database round trips at the same interface generated code uses.
type countedQueries struct {
	database.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() database.Handle {
	return d.Handle.WithExecutor(d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.DBTX.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}
