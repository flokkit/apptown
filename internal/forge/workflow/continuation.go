package workflow

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// Completed runs and their completion jobs are immutable history. Continue at
// the producing stage with a new run identity, the same pinned procedure and
// Change workspace, and fresh stage checks/approvals. Artifacts are inherited
// context (including inputs from earlier stages), never inherited gate passes.
//
// The continuation is a new root: like any, it takes the change (a landing
// runs beside the holder), and its restart point is fixed afresh when its
// folder is prepared, after whatever other workers did since the source.
func continueWorkIn(ctx context.Context, tx *database.Tx, source *Run, agent *Step, note, who, landingBy string, chats Presence, at time.Time) (Retried, error) {
	var result Retried
	spec, err := source.Definition()
	if err != nil {
		return result, err
	}
	if stage, _ := spec.Stage(agent.Stage); stage == nil {
		return result, fmt.Errorf("stage %q is missing from the pinned definition", agent.Stage)
	}
	session, err := previousAgentSessionIn(ctx, tx, agent)
	if err != nil {
		return result, err
	}
	q := workflowsql.New(tx.SQL())
	if err := ctx.Err(); err != nil {
		return result, err
	}
	number, err := q.NextRunNumber(ctx, source.ProjectID)
	if err != nil {
		return result, err
	}
	created, err := q.ContinuationInsertRun(ctx, workflowsql.ContinuationInsertRunParams{ID: ids.New(at), Number: number, At: at, Status: RunRunning.String(), Stage: agent.Stage, SourceID: source.ID})
	if err != nil {
		return result, err
	}
	id := created.ID
	// A fresh root: the source's admission authorizes nothing here.
	if err := markLandingAuthorityIn(ctx, tx, id, landingBy); err != nil {
		return result, err
	}
	admit := takeChangeForRunIn
	if spec.Role == "landing" {
		admit = landBesideHolderIn
	}
	if err := admit(ctx, tx, RunRef{ID: id, ProjectID: source.ProjectID}, chats, at); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	got, writeErr := q.ContinuationLink(ctx, workflowsql.ContinuationLinkParams{SourceRunID: source.ID, RunID: id})
	if err := continuationCount(1, got, writeErr); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Attribution follows the source owner; unattributed history names its source Run.
	got, writeErr = q.ContinuationOwner(ctx, workflowsql.ContinuationOwnerParams{RunID: id, SourceID: source.ID, FallbackActor: fmt.Sprintf("run/%d", source.ID)})
	if err := continuationCount(1, got, writeErr); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	got, writeErr = q.ContinuationStart(ctx, id)
	if err := continuationCount(1, got, writeErr); err != nil {
		return result, err
	}
	if err := recordReceiptIn(ctx, tx, id, "run.created", continuationCreated{source.WorkflowName, source.WorkflowVersion, source.ID}, at); err != nil {
		return result, err
	}
	if err := recordReceiptIn(ctx, tx, id, "run.continued", continuationReceipt{source.ID, agent.ID, agent.Stage, who, "inherited as context; checks and approvals run again"}, at); err != nil {
		return result, err
	}
	pins, err := q.ContinuationPinCount(ctx, source.ID)
	if err != nil {
		return result, err
	}
	// Pins describe inherited context, never new resolutions.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	got, writeErr = q.ContinuationCopyPins(ctx, workflowsql.ContinuationCopyPinsParams{NewIds: mintIDs(pins, at), RunID: id, SourceID: source.ID, At: at})
	if err := continuationCount(pins, got, writeErr); err != nil {
		return result, err
	}
	artifacts, err := q.ContinuationArtifactCount(ctx, source.ID)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	got, writeErr = q.ContinuationCopyArtifacts(ctx, workflowsql.ContinuationCopyArtifactsParams{NewIds: mintIDs(artifacts, at), RunID: id, SourceID: source.ID})
	if err := continuationCount(artifacts, got, writeErr); err != nil {
		return result, err
	}
	checks, err := readStageChecks(ctx, tx, source.ID, agent.Stage)
	if err != nil {
		return result, err
	}
	if err := recordReceiptIn(ctx, tx, id, "stage.entered", stageEnteredReceipt{agent.Stage, checks}, at); err != nil {
		return result, err
	}
	steps, err := q.ContinuationStepCount(ctx, workflowsql.ContinuationStepCountParams{RunID: source.ID, Stage: agent.Stage})
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Use the pinned materialized stage; outcomes, timestamps and votes start empty.
	got, writeErr = q.ContinuationCopySteps(ctx, workflowsql.ContinuationCopyStepsParams{NewIds: mintIDs(steps, at), RunID: id, SourceID: source.ID, Stage: agent.Stage})
	if err := continuationCount(steps, got, writeErr); err != nil {
		return result, err
	}
	step, err := q.ContinuationAgentStep(ctx, workflowsql.ContinuationAgentStepParams{RunID: id, Stage: agent.Stage, Name: agent.Name})
	if err != nil {
		return result, err
	}
	stepID := step.ID
	if err := ctx.Err(); err != nil {
		return result, err
	}
	got, writeErr = q.ContinuationFeedback(ctx, workflowsql.ContinuationFeedbackParams{Note: note, Session: session, ID: stepID})
	if err := continuationCount(1, got, writeErr); err != nil {
		return result, err
	}
	if err := recordReceiptIn(ctx, tx, id, "step.sendback", continuationFeedback{agent.Name, stepID, who, note, agent.ID}, at); err != nil {
		return result, err
	}
	return Retried{id, stepID}, nil
}

type continuationCreated struct {
	Workflow      string `json:"workflow"`
	Version       int    `json:"version"`
	ContinuedFrom ids.ID `json:"continuedFrom"`
}
type continuationReceipt struct {
	SourceRunID  ids.ID `json:"sourceRunId"`
	SourceStepID ids.ID `json:"sourceStepId"`
	Stage        string `json:"stage"`
	By           string `json:"by"`
	Artifacts    string `json:"artifacts"`
}
type continuationFeedback struct {
	Step         string `json:"step"`
	StepID       ids.ID `json:"stepId"`
	By           string `json:"by"`
	Note         string `json:"note"`
	SourceStepID ids.ID `json:"sourceStepId"`
}

func continuationCount(want int64, got int64, err error) error {
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("continuation write recorded %d rows, expected %d", got, want)
	}
	return nil
}

// mintIDs mints the ids of n copied rows, in the order the copy writes them.
func mintIDs(n int64, at time.Time) []ids.ID {
	out := make([]ids.ID, n)
	for i := range out {
		out[i] = ids.New(at)
	}
	return out
}
