package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type ChangeMove struct {
	Action ChangeAction
	RunID  ids.ID // attach, land or resume; zero preserves the current association
}
type changeLifecycle struct {
	ID, ProjectID, RunID ids.ID
	State                ChangeState
	Type, Branch         string
}

// The caller holds the project lock and owns commit/rollback plus required
// business receipts. A fresh Change is locked here even when admission already
// read it: earlier work in the same transaction may have cancelled its landing.
// at is AppTown's time of the move, which closes the Change when it ends.
func TransitionChangeIn(ctx context.Context, tx *database.Tx, projectID ids.ID, id ids.ID, move ChangeMove, at time.Time) (changeLifecycle, error) {
	var current changeLifecycle
	if id.IsZero() || projectID.IsZero() {
		return current, fmt.Errorf("invalid Change transition command")
	}
	row, err := changeRecord(workflowsql.New(tx.SQL()).ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: id, ProjectID: projectID}))
	if err != nil {
		return current, err
	}
	current = changeLifecycle{ID: row.ID, ProjectID: row.ProjectID, RunID: row.RunID, State: row.State, Type: row.Type, Branch: row.Branch}
	next, err := current.apply(move)
	if err != nil || next == current {
		return current, err
	}
	if !move.RunID.IsZero() {
		owned, err := workflowsql.New(tx.SQL()).IsChangeRootRun(ctx, workflowsql.IsChangeRootRunParams{ID: move.RunID, ProjectID: projectID, ChangeID: id})
		if err != nil {
			return current, err
		}
		if !owned {
			return current, fmt.Errorf("%w: run %d is not root work for Change %d", ErrChangeTransition, move.RunID, id)
		}
	}
	n, err := workflowsql.New(tx.SQL()).TransitionChange(ctx, workflowsql.TransitionChangeParams{State: next.State.String(), RunID: next.RunID, Branch: next.Branch, Closed: next.State.Closed(), At: at, ID: id, ProjectID: projectID, OldState: current.State.String(), OldRunID: current.RunID})
	if err != nil {
		return current, err
	}
	if n != 1 {
		return current, fmt.Errorf("%w: Change %d changed or its write was suppressed", ErrChangeTransition, id)
	}
	return next, nil
}

type changeStore struct {
	db *database.DB
	clocked
}
