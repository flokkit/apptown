package dbtest

import (
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/database/query"
)

var ErrInjectedFailure = database.ErrInjectedFailure

func NextStatementFailure(db *database.DB, fragment string) func() {
	return db.FailNextStatement(fragment)
}
func StatementFailures(db *database.DB, fragment string, n int) func() {
	return db.FailStatements(fragment, n)
}
func StatementFailureAfter(db *database.DB, fragment string, skip int) func() {
	return db.FailStatementAfter(fragment, skip)
}

// WithExecutor decorates execution while preserving the original pool's fault state.
func WithExecutor(handle query.Handle, executor query.Executor) query.Handle {
	handle.Executor = executor
	return handle
}
