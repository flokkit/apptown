package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

type LawViolation struct {
	Policy  string `json:"policy"`
	Rule    string `json:"rule"`
	Reading string `json:"reading"`
	Detail  string `json:"detail"`
}
type policyGateEvidence struct {
	Violations []LawViolation `json:"violations"`
	Reason     string         `json:"reason"`
}

// LawRules interprets current project rules against a pinned declaration.
// Reads use the Work transaction so gate state, evidence and receipts agree.
type LawRules interface {
	ViolationsIn(context.Context, database.Queryer, ids.ID, *schema.Spec) ([]LawViolation, error)
}
type lawStore struct {
	db    *database.DB
	rules LawRules
	clocked
}
type lawSet struct{ records lawStore }

func newLaws(db *database.DB, rules LawRules) lawSet { return lawSet{lawStore{db: db, rules: rules}} }
func (l lawSet) Refresh(ctx context.Context, run *Run) error {
	_, err := l.records.evaluate(ctx, run, true)
	return err
}

func lawNextAction(violations []LawViolation) string {
	names := make([]string, len(violations))
	for i, v := range violations {
		names[i] = v.Policy
	}
	return fmt.Sprintf("The rules changed while this landing waited (%s). "+
		"NEXT: press \"Restart landing\" — the change re-lands under the current lane and the new checks run. "+
		"To land under the OLD rules instead, override this check with a written reason (recorded).", strings.Join(names, ", "))
}

// A recorded override is standing assent for this run. An automatic pass is
// only an observation: current law is evaluated again before a later merge.
// Project/run locks serialize creation, and source reads, state, receipt and
// pause audit share this transaction. No App or external work is called here.
func (g lawStore) evaluate(ctx context.Context, expected *Run, onlyExisting bool) (*Step, error) {
	return database.Transact(ctx, g.db, func(tx *database.Tx) (*Step, error) {
		run, err := lockCurrentRunIn(ctx, tx, expected)
		if err != nil {
			return nil, err
		}
		return g.evaluateIn(ctx, tx, run, onlyExisting)
	})
}

// Admission and standalone policy checks share this transaction participant.
// The caller holds the current project/run locks; evidence and receipts remain
// part of that same commit.
func (g lawStore) evaluateIn(ctx context.Context, tx *database.Tx, run *Run, onlyExisting bool) (*Step, error) {
	now := g.now()
	q := workflowsql.New(tx.SQL())
	steps, err := stepRecords(q.StepsOfTypeForUpdate(ctx, workflowsql.StepsOfTypeForUpdateParams{RunID: run.ID, Type: "policy/current"}))
	if err != nil {
		return nil, err
	}
	var current *Step
	for _, step := range steps {
		if step.Status == StatusPass && step.OverrideBy != "" {
			return nil, nil
		}
		if step.Stage == run.CurrentStage {
			if current != nil {
				return nil, fmt.Errorf("run %d has multiple current-policy checks in stage %q", run.ID, run.CurrentStage)
			}
			current = step
		}
	}
	if current == nil && onlyExisting {
		return nil, nil
	}
	spec, err := run.Definition()
	if err != nil {
		return nil, err
	}
	if stage, _ := spec.Stage(run.CurrentStage); stage == nil {
		return nil, fmt.Errorf("run %d stage %q is missing from its pinned definition", run.ID, run.CurrentStage)
	}
	if g.rules == nil {
		return nil, fmt.Errorf("current project rules are unavailable")
	}
	violations, err := g.rules.ViolationsIn(ctx, tx, run.ProjectID, spec)
	if err != nil {
		return nil, err
	}
	if violations == nil {
		violations = []LawViolation{}
	}
	if len(violations) == 0 && current == nil {
		return nil, nil
	}
	previous := StatusPending
	if current != nil {
		previous = current.Status
	}
	next, reason := StatusPass, "the policy amendment no longer applies"
	if len(violations) > 0 {
		next, reason = StatusNeedsHuman, lawNextAction(violations)
	}
	next, err = previous.Transition(MoveStepEvaluate, next)
	if err != nil {
		return nil, err
	}
	if current != nil && next == previous {
		var saved policyGateEvidence
		if json.Unmarshal(current.Evidence, &saved) == nil && saved.Reason == reason && slices.Equal(saved.Violations, violations) {
			if next == StatusPass {
				return nil, nil
			}
			return current, nil
		}
	}
	body, err := json.Marshal(policyGateEvidence{violations, reason})
	if err != nil {
		return nil, err
	}
	if current == nil {
		current, err = stepResult(q.CreateWorkflowGate(ctx, workflowsql.CreateWorkflowGateParams{ID: ids.New(now), At: now, RunID: run.ID, Stage: run.CurrentStage, Name: "current-rules", Type: "policy/current", Status: next.String(), EvidenceJson: []byte(body)}))
		if err != nil {
			return nil, err
		}
	} else {
		n, err := q.RecordStepEvaluation(ctx, workflowsql.RecordStepEvaluationParams{At: now, ID: current.ID, Attempt: current.Attempt, ExpectedStatus: previous.String(), Status: next.String(), EvidenceJson: []byte(body)})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrRunMoved
		}
	}
	if next != previous {
		if next == StatusNeedsHuman {
			receipt := struct {
				Violations []LawViolation `json:"violations"`
			}{violations}
			if err := recordReceiptIn(ctx, tx, run.ID, "law.changed", receipt, now); err != nil {
				return nil, err
			}
			if err := audit.At(now).RecordIn(ctx, tx, "apptown-system", run.ProjectID, "landing.paused-by-law", fmt.Sprintf("run %d", run.ID), receipt); err != nil {
				return nil, err
			}
		} else {
			if err := recordReceiptIn(ctx, tx, run.ID, "law.satisfied", struct {
				Reason string `json:"reason"`
			}{reason}, now); err != nil {
				return nil, err
			}
		}
	}
	if next == StatusPass {
		return nil, nil
	}
	return stepResult(q.StepByID(ctx, current.ID))
}
