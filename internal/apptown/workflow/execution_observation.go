package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"

	"github.com/flokkit/apptown/internal/database"
)

// ExecutionSelection is an already-admitted Change's tracked execution or newest
// root landing. The caller supplies its snapshot/locked transaction; Work reads
// only records belonging to both the selected project and Change.
type ExecutionSelection struct {
	Change        ChangeRef
	RunID         ids.ID
	LatestLanding bool
}

// ExecutionObservation reuses canonical Work records. It conveys no attempt
// claim. Steps belong only to the Run's currently recorded stage.
type ExecutionObservation struct {
	Run     Run
	Steps   []*Step
	Unready bool
	Reworks int
}

// Same compares all recorded facts before an external owner settles its own
// outcome. A changed definition, scope, attempt, evidence or preparation receipt
// invalidates the earlier observation as well as a changed lifecycle/revision.
func (o ExecutionObservation) Same(other ExecutionObservation) bool {
	return o.Run == other.Run && o.Unready == other.Unready && o.Reworks == other.Reworks && reflect.DeepEqual(o.Steps, other.Steps)
}

func ReadChangeInProject(ctx context.Context, q database.Queryer, ref ChangeRef) (*Change, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, sql.ErrNoRows
	}
	return readChangeInProject(ctx, q, ref.ID, ref.ProjectID)
}

func ObserveExecutionIn(ctx context.Context, tx *database.Tx, in ExecutionSelection) (*ExecutionObservation, error) {
	if in.Change.ID.IsZero() || in.Change.ProjectID.IsZero() || (in.LatestLanding && !in.RunID.IsZero()) {
		return nil, fmt.Errorf("execution observation requires one scoped selection")
	}
	id := in.RunID
	if in.LatestLanding {
		var err error
		id, err = workflowsql.New(tx.SQL()).LatestLandingRunMeta(ctx, workflowsql.LatestLandingRunMetaParams{ProjectID: in.Change.ProjectID, ChangeID: in.Change.ID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if id.IsZero() {
		return nil, nil
	}
	run, err := readScopedRun(ctx, tx, id, in.Change.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if run.ChangeID != in.Change.ID {
		return nil, nil
	}
	out := &ExecutionObservation{Run: *run}
	readiness, err := workflowsql.New(tx.SQL()).ExecutionReworks(ctx, workflowsql.ExecutionReworksParams{RunID: id, ChangeID: in.Change.ID, ProjectID: in.Change.ProjectID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.Reworks = int(readiness)
	if run.Status == RunRunning {
		out.Unready, err = runNeedsPreparation(ctx, tx, id)
		if err != nil {
			return nil, err
		}
	}
	out.Steps, err = readStageSteps(ctx, tx, id, run.CurrentStage)
	if err != nil {
		return nil, err
	}
	return out, nil
}
