package workflow

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// PendingStep is the first outstanding Step in a run, ordered by running work,
// work needing a person, then pending work, with ID as a stable tie-breaker. It
// includes older materialized stages for progress displays, not execution claims.
type PendingStep struct {
	ID         ids.ID
	Attempt    int64
	Name, Type string
	State      StepState
}

// PendingStepsIn observes all requested project/run pairs in one read. Missing
// runs, wrong projects and runs with no outstanding Step have no entry. The
// caller supplies already-admitted scopes; this is not a public authorization API.
func PendingStepsIn(ctx context.Context, q database.Queryer, refs []RunRef) (map[RunRef]PendingStep, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[RunRef]PendingStep)
	var idList, projects []ids.ID
	seen := make(map[RunRef]bool, len(refs))
	for _, ref := range refs {
		if ref.ID.IsZero() || ref.ProjectID.IsZero() {
			return nil, fmt.Errorf("pending steps require scoped runs")
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
	rows, err := workflowsql.New(q.SQL()).PendingStepsForScopes(ctx, workflowsql.PendingStepsForScopesParams{RunIds: idList, ProjectIds: projects,
		RunningState: StatusRunning.String(), HumanState: StatusNeedsHuman.String(), PendingState: StatusPending.String()})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		state, err := parseStepState(row.Status)
		if err != nil {
			return nil, err
		}
		out[RunRef{ID: row.RunID, ProjectID: row.ProjectID}] = PendingStep{ID: row.StepID, Attempt: row.Attempt, Name: row.Name, Type: row.Type, State: state}
	}
	return out, nil
}
