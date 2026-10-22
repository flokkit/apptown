package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

var errMergeOverride = errors.New("the recorded override is preserved, but the merge is still blocked; resolve the merge problem and recheck")

type mergeGateStore struct {
	db *database.DB
	clocked
}

type preparedMergeGate struct {
	db     *database.DB
	run    *Run
	steps  []*Step
	source MergeSource
	clocked
}

// Capture the gates before calling Git. No connection or lock escapes this
// transaction. A later outcome cannot overwrite a newer check, retry or override.
func (g mergeGateStore) prepare(ctx context.Context, expected *Run) (*preparedMergeGate, error) {
	return database.Transact(ctx, g.db, func(tx *database.Tx) (*preparedMergeGate, error) {
		run, err := lockCurrentRunIn(ctx, tx, expected)
		if err != nil {
			return nil, err
		}
		source, err := readMergeSourceForRun(ctx, tx, run)
		if err != nil {
			return nil, err
		}
		return prepareMergeGateIn(ctx, tx, g.db, run, source, g.clocked)
	})
}

func mergeGatesIn(ctx context.Context, tx *database.Tx, runID ids.ID) ([]*Step, error) {
	return stepRecords(workflowsql.New(tx.SQL()).StepsOfTypeForUpdate(ctx, workflowsql.StepsOfTypeForUpdateParams{RunID: runID, Type: "scm/merged"}))
}

func (p *preparedMergeGate) lock(ctx context.Context, tx *database.Tx) error {
	if _, err := lockCurrentRunIn(ctx, tx, p.run); err != nil {
		return err
	}
	steps, err := mergeGatesIn(ctx, tx, p.run.ID)
	if err != nil {
		return err
	}
	source, err := readMergeSourceForRun(ctx, tx, p.run)
	if err != nil {
		return err
	}
	if source != p.source {
		return ErrRunMoved
	}
	if !slices.EqualFunc(p.steps, steps, SameStep) {
		return ErrRunMoved
	}
	return nil
}

func (p *preparedMergeGate) block(ctx context.Context, evidence MergeEvidence) (*Step, error) {
	return database.Transact(ctx, p.db, func(tx *database.Tx) (*Step, error) {
		if err := p.lock(ctx, tx); err != nil {
			return nil, err
		}
		var current *Step
		for _, step := range p.steps {
			if step.Stage == p.run.CurrentStage {
				current = step
			}
		}
		previous := StatusPending
		if current != nil {
			// An exception is a standing human decision, not proof of a physical
			// merge. Preserve it and return the refusal to the caller explicitly.
			if current.OverrideBy != "" {
				return nil, errMergeOverride
			}
			previous = current.Status
		}
		next, err := previous.Transition(MoveStepEvaluate, StatusNeedsHuman)
		if err != nil {
			return nil, err
		}
		q := workflowsql.New(tx.SQL())
		body, err := json.Marshal(evidence)
		if err != nil {
			return nil, err
		}
		if current == nil {
			current, err = stepResult(q.CreateWorkflowGate(ctx, workflowsql.CreateWorkflowGateParams{ID: ids.New(p.now()), At: p.now(), RunID: p.run.ID, Stage: p.run.CurrentStage, Name: "merge-to-main", Type: "scm/merged", Status: next.String(), EvidenceJson: body}))
			if err != nil {
				return nil, err
			}
		} else if err := requireAttemptWrite(q.RecordStepEvaluation(ctx, workflowsql.RecordStepEvaluationParams{At: p.now(), ID: current.ID, Attempt: current.Attempt, ExpectedStatus: previous.String(), Status: next.String(), EvidenceJson: body})); err != nil {
			return nil, err
		}
		kind, files := "merge.conflict", evidence.Conflicts
		if len(files) == 0 {
			kind = "merge.blocked"
		}
		if len(evidence.OutOfBandFiles) > 0 {
			kind, files = "integrity.out-of-band-edits", evidence.OutOfBandFiles
		}
		// Distinguish Work admission refusals from actual Git conflicts.
		if err := recordReceiptIn(ctx, tx, p.run.ID, kind, struct {
			Files  []string `json:"files,omitempty"`
			Reason string   `json:"reason,omitempty"`
		}{files, evidence.Reason}, p.now()); err != nil {
			return nil, err
		}
		return stepResult(q.StepByID(ctx, current.ID))
	})
}

func (p *preparedMergeGate) clear(ctx context.Context) error {
	_, err := database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		if err := p.lock(ctx, tx); err != nil {
			return struct{}{}, err
		}
		q := workflowsql.New(tx.SQL())
		for _, step := range p.steps {
			if step.Status == StatusPass || step.OverrideBy != "" {
				continue
			}
			next, err := step.Status.Transition(MoveStepEvaluate, StatusPass)
			if err != nil {
				return struct{}{}, err
			}
			if err := requireAttemptWrite(q.RecordStepEvaluation(ctx, workflowsql.RecordStepEvaluationParams{At: p.now(), ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String(), Status: next.String(), EvidenceJson: json.RawMessage(`{"merged":true}`)})); err != nil {
				return struct{}{}, err
			}
			if err := recordReceiptIn(ctx, tx, p.run.ID, "check.evaluated", stepEvaluationReceipt{step.Name, next}, p.now()); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func prepareMergeGateIn(ctx context.Context, tx *database.Tx, db *database.DB, run *Run, source MergeSource, at clocked) (*preparedMergeGate, error) {
	steps, err := mergeGatesIn(ctx, tx, run.ID)
	if err != nil {
		return nil, err
	}
	current := 0
	for _, step := range steps {
		if _, err := step.Status.Transition(MoveStepEvaluate, StatusPass); err != nil {
			return nil, err
		}
		if step.Stage == run.CurrentStage {
			current++
		}
	}
	if current > 1 {
		return nil, fmt.Errorf("run %d has multiple merge checks in stage %q", run.ID, run.CurrentStage)
	}
	return &preparedMergeGate{db, run, steps, source, at}, nil
}
