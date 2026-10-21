package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/stamp"
)

func runRecord(row workflowsql.Run, err error) (*Run, error) {
	if err != nil {
		return nil, database.QueryError(err, "reading workflow run")
	}
	state, err := ParseRunState(row.Status)
	if err != nil {
		return nil, err
	}
	return &Run{ID: row.ID, Number: row.Number, ProjectID: row.ProjectID, WorkflowName: row.WorkflowName,
		WorkflowVersion: int(row.WorkflowVersion), Kind: row.Kind, Status: state,
		CurrentStage: row.CurrentStage, DefinitionJSON: string(row.DefinitionJson),
		ChangeID: row.ChangeID, ParentRunID: row.ParentRunID, ParentStepID: row.ParentStepID,
		ParentStepAttempt: row.ParentStepAttempt, InputsJSON: string(row.InputsJson),
		AdoptionID: row.AdoptionID, RetryRevision: row.RetryRevision,
		CreatedAt: row.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.Format(time.RFC3339Nano)}, nil
}

// A read never assigns ownedAttempt. Only committed admission can grant a
// runner the authority to publish that attempt's result.
func stepResult(row workflowsql.Step, err error) (*Step, error) {
	if err != nil {
		return nil, database.QueryError(err, "reading workflow step")
	}
	return stepRecord(row)
}

func stepRecord(row workflowsql.Step) (*Step, error) {
	state, err := parseStepState(row.Status)
	if err != nil {
		return nil, err
	}
	return &Step{ID: row.ID, RunID: row.RunID, Stage: row.Stage, Name: row.Name, Type: row.Type,
		Attempt: row.Attempt, Status: state, Config: rawJSON(string(row.ConfigJson)),
		Evidence: rawJSON(string(row.EvidenceJson)), OverrideBy: row.OverrideBy, OverrideReason: row.OverrideReason,
		StartedAt: stamp.Time{NullTime: row.StartedAt}, FinishedAt: stamp.Time{NullTime: row.FinishedAt}}, nil
}

func stepRecords(rows []workflowsql.Step, err error) ([]*Step, error) {
	if err != nil {
		return nil, database.QueryError(err, "reading workflow steps")
	}
	var result []*Step
	for _, row := range rows {
		step, err := stepRecord(row)
		if err != nil {
			return nil, err
		}
		result = append(result, step)
	}
	return result, nil
}

func readRun(ctx context.Context, db database.Queryer, id ids.ID) (*Run, error) {
	run, err := runRecord(workflowsql.New(db.SQL()).RunByID(ctx, workflowsql.RunByIDParams{ID: id}))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missingRun{id}
	}
	return run, err
}
func readStep(ctx context.Context, db database.Queryer, id ids.ID) (*Step, error) {
	step, err := stepResult(workflowsql.New(db.SQL()).StepByID(ctx, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missingStep{id}
	}
	return step, err
}
func readSteps(ctx context.Context, db database.Queryer, id ids.ID) ([]*Step, error) {
	rows, err := stepRecords(workflowsql.New(db.SQL()).RunSteps(ctx, workflowsql.RunStepsParams{RunID: id}))
	if err == nil && rows == nil {
		rows = []*Step{}
	}
	return rows, err
}
func lockStep(ctx context.Context, tx *database.Tx, id ids.ID) (*Run, *Step, error) {
	q := workflowsql.New(tx.SQL())
	identity, err := q.StepRunIdentityMeta(ctx, id)
	if err != nil {
		return nil, nil, database.Missing(err)
	}
	projectID, runID := identity.ProjectID, identity.RunID
	if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
		return nil, nil, err
	}
	run, err := runRecord(q.RunForUpdate(ctx, runID))
	if err != nil {
		return nil, nil, err
	}
	step, err := stepResult(q.StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: id}))
	if err != nil {
		return nil, nil, err
	}
	if run.ProjectID != projectID || step.RunID != run.ID {
		return nil, nil, ErrStepAttemptLost
	}
	return run, step, nil
}

// Keep JSON exact while matching the compact wire form used by detached claims.
func rawJSON(s string) json.RawMessage {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		return json.RawMessage(s)
	}
	return json.RawMessage(b.Bytes())
}

func readScopedRun(ctx context.Context, db database.Queryer, id ids.ID, project ids.ID) (*Run, error) {
	return runRecord(workflowsql.New(db.SQL()).RunByID(ctx, workflowsql.RunByIDParams{ID: id, ProjectID: project}))
}

func readStageSteps(ctx context.Context, db database.Queryer, id ids.ID, stage string) ([]*Step, error) {
	return stepRecords(workflowsql.New(db.SQL()).RunSteps(ctx, workflowsql.RunStepsParams{RunID: id, StageOnly: true, Stage: stage}))
}

// These observations serve already-admitted execution adapters. User-facing reads
// go through Service so the stored project scope is authorized before publication.
func ReadRunIn(ctx context.Context, db database.Queryer, id ids.ID) (*Run, error) {
	return readRun(ctx, db, id)
}
func ReadStepIn(ctx context.Context, db database.Queryer, id ids.ID) (*Step, error) {
	return readStep(ctx, db, id)
}
func ReadStageStepsIn(ctx context.Context, db database.Queryer, id ids.ID, stage string) ([]*Step, error) {
	return readStageSteps(ctx, db, id, stage)
}
