package workflow

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type ChangeMove struct {
	Action     ChangeAction
	RunID      ids.ID // attach, land or resume; zero preserves the current association
	NameBranch bool   // start only, from the locked project's repository configuration
}
type ChangeLifecycle struct {
	ID, ProjectID, RunID ids.ID
	State                ChangeState
	Type, Branch         string
}

// The caller holds the project lock and owns commit/rollback plus required
// business receipts. A fresh Change is locked here even when admission already
// read it: earlier work in the same transaction may have cancelled its landing.
// at is the forge's time of the move, which closes the Change when it ends.
func TransitionChangeIn(ctx context.Context, tx *database.Tx, projectID ids.ID, id ids.ID, move ChangeMove, at time.Time) (ChangeLifecycle, error) {
	var current ChangeLifecycle
	if id.IsZero() || projectID.IsZero() {
		return current, fmt.Errorf("invalid Change transition command")
	}
	row, err := changeRecord(workflowsql.New(tx.SQL()).ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: id, ProjectID: projectID}))
	if err != nil {
		return current, err
	}
	current = ChangeLifecycle{ID: row.ID, ProjectID: row.ProjectID, RunID: row.RunID, State: row.State, Type: row.Type, Branch: row.Branch}
	next, err := current.Apply(move)
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

// Promotion, branch naming and the required receipt commit before any workspace
// effect. Repeating start while in progress is allowed; terminal work never reopens.
func (s changeStore) start(ctx context.Context, projectID ids.ID, id ids.ID, who string) (*Change, error) {
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	project, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := refuseUnlessActive(ctx, tx, projectID); err != nil {
		return nil, err
	}
	if _, err := TransitionChangeIn(ctx, tx, projectID, id, ChangeMove{Action: StartChange, NameBranch: project.DefaultBranch != ""}, now); err != nil {
		return nil, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, who, projectID, "change.started", fmt.Sprintf("change %d", id), nil); err != nil {
		return nil, err
	}
	change, err := readChange(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return change, nil
}
