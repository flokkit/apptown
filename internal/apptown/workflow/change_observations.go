package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// ChangeStatesIn reads a set of admitted project/Change pairs in one statement.
// Missing or foreign records are absent; read/codec errors never become absence.
func ChangeStatesIn(ctx context.Context, q database.Queryer, refs []ChangeRef) (map[ChangeRef]ChangeState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[ChangeRef]ChangeState)
	var idList, projects []ids.ID
	seen := make(map[ChangeRef]bool, len(refs))
	for _, ref := range refs {
		if ref.ID.IsZero() || ref.ProjectID.IsZero() {
			return nil, fmt.Errorf("Change state requires project scope")
		}
		if !seen[ref] {
			idList = append(idList, ref.ID)
			projects = append(projects, ref.ProjectID)
			seen[ref] = true
		}
	}
	if len(idList) == 0 {
		return out, nil
	}
	rows, err := workflowsql.New(q.SQL()).ChangeStatesForScopes(ctx, workflowsql.ChangeStatesForScopesParams{Column1: idList, Column2: projects})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		state, err := ParseChangeState(row.State)
		if err != nil {
			return nil, err
		}
		out[ChangeRef{ID: row.ID, ProjectID: row.ProjectID}] = state
	}
	return out, nil
}

// SupersessionEligibleIn locks project before Change and retains both locks in
// the supplied transaction. Only in-progress work can be superseded by a fresh
// attempt. The caller still records abandonment and the summary through Work.
func SupersessionEligibleIn(ctx context.Context, tx *database.Tx, ref ChangeRef) (bool, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return false, fmt.Errorf("supersession requires project scope")
	}
	state, err := workflowsql.New(tx.SQL()).SupersessionChangeStateForUpdate(ctx, workflowsql.SupersessionChangeStateForUpdateParams{ChangeID: ref.ID, ProjectID: ref.ProjectID})
	if err != nil {
		return false, err
	}
	parsed, err := ParseChangeState(state)
	return parsed == ChangeInProgress, err
}

// ProjectBlocker identifies a Change occupying the project. Zero ID means work
// without a Change. Number is the Change's project-local display number.
type ProjectBlocker struct {
	ID     ids.ID `json:"id"`
	Number int64  `json:"number"`
	Kind   string `json:"kind"` // landing or step; landing takes precedence when both apply
}

// ProjectBlockersIn observes Work's durable occupancy for admission and reads.
// A landing Change or a running Step occupies the project. Runs waiting on a
// person do not. Admission holds the project lock and composes its own worker
// and capacity policy. Errors never become an unoccupied project.
func ProjectBlockersIn(ctx context.Context, db database.Queryer, projectID ids.ID) ([]ProjectBlocker, error) {
	if projectID.IsZero() {
		return nil, fmt.Errorf("occupancy requires valid project scope")
	}
	rows, err := workflowsql.New(db.SQL()).ProjectWorkBlockers(ctx, workflowsql.ProjectWorkBlockersParams{ProjectID: projectID,
		LandingState: ChangeLanding.String(), RunningStepState: StatusRunning.String()})
	if err != nil {
		return nil, err
	}
	out := make([]ProjectBlocker, 0, len(rows))
	for _, row := range rows {
		out = append(out, ProjectBlocker{ID: row.ID, Number: row.Number, Kind: row.Kind})
	}
	return out, nil
}

// SettledSince says whether any change closed, or any run stopped running, at
// or after at (AppTown's clock stamps both). Nothing else makes a change's or
// a run's workspace newly finished, so a sweep that removes finished
// workspaces' files has nothing new to find when this is false.
func SettledSince(ctx context.Context, db database.Queryer, at time.Time) (bool, error) {
	return workflowsql.New(db.SQL()).SettledSince(ctx, workflowsql.SettledSinceParams{Since: at, Running: RunRunning.String()})
}
