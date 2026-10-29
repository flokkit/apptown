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

// ChangeIDByNumberIn is the id of the project's change with that number,
// the number people read and type ("egg-catch#12"). A number no change has
// is not found, in words naming the friendly form.
func ChangeIDByNumberIn(ctx context.Context, q database.Queryer, projectID ids.ID, slug string, number int64) (ids.ID, error) {
	id, err := workflowsql.New(q.SQL()).ChangeIDByNumber(ctx, workflowsql.ChangeIDByNumberParams{ProjectID: projectID, Number: number})
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, missingNumbered{"change", slug, number}
	}
	return id, err
}

// RunIDByNumberIn is the id of the project's run with that number ("run 40",
// typed "egg-catch#40" where a run is expected). A number no run has is not
// found, in words naming the friendly form.
func RunIDByNumberIn(ctx context.Context, q database.Queryer, projectID ids.ID, slug string, number int64) (ids.ID, error) {
	id, err := workflowsql.New(q.SQL()).RunIDByNumber(ctx, workflowsql.RunIDByNumberParams{ProjectID: projectID, Number: number})
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, missingNumbered{"run", slug, number}
	}
	return id, err
}

type missingNumbered struct {
	what   string // change or run
	slug   string
	number int64
}

func (e missingNumbered) Error() string {
	return fmt.Sprintf("no %s %s#%d", e.what, e.slug, e.number)
}
func (missingNumbered) FaultKind() fault.Kind { return fault.NotFound }
func (missingNumbered) Unwrap() error         { return sql.ErrNoRows }
