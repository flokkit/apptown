package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// StepOutcome carries runner-owned evidence and optional receipt details as exact
// JSON. Step identity, status and attempt in the receipt always come from Work.
type StepOutcome struct {
	Rework   bool // failure should request the stage's permitted rework
	Status   StepState
	Evidence json.RawMessage
	Details  json.RawMessage
	// Artifacts are the step's products, recorded in the transaction that
	// records the outcome. A later step that reads a passing producer finds
	// its artifacts; a failed artifact write records no outcome at all.
	Artifacts []StepArtifact
}

// StepArtifact is one artifact recorded with a step's outcome. By, when
// set, also records an artifact.added receipt naming its author, for the
// artifacts later steps consume by kind.
type StepArtifact struct {
	ArtifactDraft
	By string
}
type finishedAttempt struct {
	Run  *Run
	Step *Step
}

func (s stepAttempts) Finish(ctx context.Context, claim *Step, outcome StepOutcome) error {
	if claim == nil || claim.OwnedAttempt() == 0 {
		return ErrStepAttemptLost
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error { _, err := s.FinishIn(ctx, tx, claim, outcome); return err })
}

// FinishIn lets rework commit outcome and continuation together. It owns current
// scope lookup/locking rather than accepting caller-supplied current database rows.
func (s stepAttempts) FinishIn(ctx context.Context, tx *database.Tx, claim *Step, outcome StepOutcome) (finishedAttempt, error) {
	if claim == nil {
		return finishedAttempt{}, ErrStepAttemptLost
	}
	owned := *claim
	if owned.OwnedAttempt() == 0 {
		return finishedAttempt{}, ErrStepAttemptLost
	}
	evidence, details, err := freezeOutcome(outcome)
	if err != nil {
		return finishedAttempt{}, err
	}
	run, current, err := lockStep(ctx, tx, owned.ID)
	if err != nil {
		return finishedAttempt{}, err
	}
	if owned.ownedProjectID != run.ProjectID || owned.RunID != run.ID || current.Attempt != owned.OwnedAttempt() || current.Status != StatusRunning || current.OverrideBy != "" || run.Status != RunRunning || run.CurrentStage != current.Stage {
		return finishedAttempt{}, fmt.Errorf("step %d: %w", current.ID, ErrStepAttemptLost)
	}
	if _, err := current.Status.Transition(MoveStepFinish, outcome.Status); err != nil {
		return finishedAttempt{}, err
	}
	// One time for the finish and what it made, on the forge's clock.
	at := s.now()
	if err := requireAttemptWrite(workflowsql.New(tx.SQL()).FinishAttempt(ctx, workflowsql.FinishAttemptParams{At: at, Status: outcome.Status.String(), EvidenceJson: evidence, ID: current.ID, Attempt: current.Attempt})); err != nil {
		return finishedAttempt{}, err
	}
	// Merge only exact extension values. Protected fields overwrite any caller keys.
	fields := struct {
		Step    string    `json:"step"`
		Status  StepState `json:"status"`
		Attempt int64     `json:"attempt"`
	}{current.Name, outcome.Status, current.Attempt}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return finishedAttempt{}, err
	}
	if err := json.Unmarshal(encoded, &details); err != nil {
		return finishedAttempt{}, err
	}
	payload, err := json.Marshal(details)
	if err != nil {
		return finishedAttempt{}, err
	}
	if err := recordEventIn(ctx, tx, run.ID, "step.finished", payload, s.now()); err != nil {
		return finishedAttempt{}, err
	}
	// The attempt fence above makes these writes replay-safe: a replayed
	// settlement of the same attempt finds the step no longer running and
	// rolls back before writing anything, so each artifact of a (run, step,
	// attempt) is written exactly once.
	for _, artifact := range outcome.Artifacts {
		if err := addStepArtifactIn(ctx, tx, run.ID, artifact, at); err != nil {
			return finishedAttempt{}, fmt.Errorf("step %d: recording artifact %s %q: %w", current.ID, artifact.Kind, artifact.Name, err)
		}
	}
	finished, err := stepResult(workflowsql.New(tx.SQL()).StepByID(ctx, current.ID))
	if err != nil {
		return finishedAttempt{}, err
	}
	return finishedAttempt{Run: run, Step: finished}, nil
}
func addStepArtifactIn(ctx context.Context, tx *database.Tx, runID ids.ID, in StepArtifact, at time.Time) error {
	if in.Kind == "" {
		return fmt.Errorf("artifact kind is required")
	}
	id, err := AddArtifactIn(ctx, tx, runID, in.Kind, in.Name, in.Ref, in.Body, at)
	if err != nil || in.By == "" {
		return err
	}
	return recordReceiptIn(ctx, tx, runID, "artifact.added", struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		ID   ids.ID `json:"id"`
		By   string `json:"by"`
	}{in.Kind, in.Name, id, in.By}, at)
}
func freezeOutcome(in StepOutcome) (json.RawMessage, map[string]json.RawMessage, error) {
	evidence := in.Evidence
	if len(evidence) == 0 {
		evidence = json.RawMessage(`null`)
	}
	if !json.Valid(evidence) {
		return nil, nil, fmt.Errorf("Step outcome evidence must be valid JSON")
	}
	details := map[string]json.RawMessage{}
	if len(in.Details) != 0 {
		if err := json.Unmarshal(in.Details, &details); err != nil {
			return nil, nil, fmt.Errorf("Step outcome details must be a JSON object: %w", err)
		}
		if details == nil {
			details = map[string]json.RawMessage{}
		}
	}
	return append(json.RawMessage(nil), evidence...), details, nil
}

// AbandonedWhileRunning is the reason a step ends with when its run was
// abandoned while it ran.
const AbandonedWhileRunning = "the run was abandoned while this step ran"

// endAbandoned ends claim's attempt in error if its run has been abandoned
// and the step is still running under that attempt; otherwise it does
// nothing. The ending and its receipt commit together.
func (s stepAttempts) endAbandoned(ctx context.Context, claim *Step) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		run, current, err := lockStep(ctx, tx, claim.ID)
		if err != nil {
			return err
		}
		if run.Status != RunAbandoned || current.Status != StatusRunning || current.Attempt != claim.OwnedAttempt() {
			return nil
		}
		return endAbandonedStepIn(ctx, tx, run.ID, current, s.now())
	})
}

// endAbandonedStepIn ends one running step of an abandoned run in error,
// with its receipt.
func endAbandonedStepIn(ctx context.Context, tx *database.Tx, runID ids.ID, step *Step, at time.Time) error {
	next, err := step.Status.Transition(MoveStepFinish, StatusError)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(map[string]string{"reason": AbandonedWhileRunning})
	if err != nil {
		return err
	}
	if err := requireAttemptWrite(workflowsql.New(tx.SQL()).FinishAttempt(ctx, workflowsql.FinishAttemptParams{At: at, Status: next.String(), EvidenceJson: evidence, ID: step.ID, Attempt: step.Attempt})); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Step    string    `json:"step"`
		Status  StepState `json:"status"`
		Attempt int64     `json:"attempt"`
		Reason  string    `json:"reason"`
	}{step.Name, next, step.Attempt, AbandonedWhileRunning})
	if err != nil {
		return err
	}
	return recordEventIn(ctx, tx, runID, "step.finished", payload, at)
}
