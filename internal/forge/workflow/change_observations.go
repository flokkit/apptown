package workflow

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
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

type OccupancyScope struct{ ProjectID, ReplacingChangeID ids.ID }

// ProjectBusyIn observes Work's durable occupancy. The one replaced Change may
// have waiting runs, but any running Step still blocks a fresh attempt. Admission
// holds the project lock and composes its own worker/lease/capacity policy.
func ProjectBusyIn(ctx context.Context, tx *database.Tx, in OccupancyScope) (bool, error) {
	if in.ProjectID.IsZero() {
		return false, fmt.Errorf("occupancy requires valid project scope")
	}
	return workflowsql.New(tx.SQL()).ProjectWorkBusy(ctx, workflowsql.ProjectWorkBusyParams{ProjectID: in.ProjectID, ReplacingChangeID: in.ReplacingChangeID,
		LandingState: ChangeLanding.String(), RunningRunState: RunRunning.String(), RunningStepState: StatusRunning.String()})
}
