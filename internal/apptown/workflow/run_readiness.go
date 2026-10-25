package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type runReadinessError struct{ missing bool }

func (e runReadinessError) Error() string {
	if e.missing {
		return "the run has no recorded start; abandon it and start new work"
	}
	return "the run has not finished preparation"
}
func (runReadinessError) FaultKind() fault.Kind { return fault.Conflict }
func (e runReadinessError) FaultCode() string {
	if e.missing {
		return "run-readiness-missing"
	}
	return "run-not-ready"
}

// Missing start evidence is not permission to repeat workspace preparation.
// Completed historical sources are read without this active execution check.
func runNeedsPreparation(ctx context.Context, db database.Queryer, id ids.ID) (bool, error) {
	start, err := workflowsql.New(db.SQL()).RunStartByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, runReadinessError{missing: true}
	}
	if err != nil {
		return false, err
	}
	return !start.Ready, nil
}
func requireRunReady(ctx context.Context, db database.Queryer, id ids.ID) error {
	pending, err := runNeedsPreparation(ctx, db, id)
	if err != nil {
		return fmt.Errorf("run %s: %w", id, err)
	}
	if pending {
		return fmt.Errorf("run %s: %w", id, runReadinessError{})
	}
	return nil
}
