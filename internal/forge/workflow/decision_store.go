package workflow

import (
	"bytes"
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type stepDecisionStore struct {
	db *database.DB
	clocked
}
type stepDecisionSnapshot struct {
	scope             decisionScope
	attempt, revision int64
	stage, stepType   string
	config            json.RawMessage
}

func (s stepDecisionStore) prepare(ctx context.Context, id ids.ID) (stepDecisionSnapshot, error) {
	row, err := workflowsql.New(s.db.SQL()).StepDecisionSnapshotMeta(ctx, id)
	if err != nil {
		return stepDecisionSnapshot{}, database.Missing(err)
	}
	return stepDecisionSnapshot{scope: decisionScope{StepID: row.StepID, RunID: row.RunID, ProjectID: row.ProjectID, ChangeID: row.ChangeID},
		attempt: row.Attempt, revision: row.RetryRevision, stage: row.CurrentStage, stepType: row.Type, config: rawJSON(string(row.ConfigJson))}, nil
}

// matches checks the facts authorized before a command acquires its locks.
// Votes and outcomes deliberately remain current: they are not admission facts.
func (p stepDecisionSnapshot) matches(run *Run, step *Step) bool {
	return p.scope == (decisionScope{step.ID, run.ID, run.ProjectID, run.ChangeID}) && step.Attempt == p.attempt && run.RetryRevision == p.revision && run.CurrentStage == p.stage && step.Type == p.stepType && bytes.Equal(step.Config, p.config)
}

func (p stepDecisionSnapshot) lock(ctx context.Context, tx *database.Tx) (*Run, *Step, error) {
	run, step, err := lockStep(ctx, tx, p.scope.StepID)
	if err != nil {
		return nil, nil, err
	}
	if err := refuseUnlessActive(ctx, tx, run.ProjectID); err != nil {
		return nil, nil, err
	}
	if !p.matches(run, step) {
		return nil, nil, ErrStepDecisionConflict
	}
	return run, step, nil
}
func (s stepDecisionStore) override(ctx context.Context, expected stepDecisionSnapshot, who, reason string) (*Step, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Step, error) {
		run, current, err := expected.lock(ctx, tx)
		if err != nil {
			return nil, err
		}
		if err := run.Decides(current); err != nil {
			return nil, err
		}
		if _, err := current.Override(who, reason); err != nil {
			return nil, err
		}
		overrider, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return nil, err
		}
		n, err := workflowsql.New(tx.SQL()).OverrideStep(ctx, workflowsql.OverrideStepParams{At: s.now(), ID: current.ID, Attempt: current.Attempt, ExpectedStatus: current.Status.String(), OverrideBy: who, OverrideByPrincipal: overrider.Principal, OverrideByVia: overrider.Via, OverrideReason: reason})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrStepDecisionConflict
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "override", overrideReceipt{current.Name, who, reason, current.Attempt}, s.now()); err != nil {
			return nil, err
		}
		return stepResult(workflowsql.New(tx.SQL()).StepByID(ctx, current.ID))
	})
}

type stepApprovalResult struct {
	step   *Step
	rework reworkedResult
}

// approve records a vote. accepted is the commit an acceptance was given
// on; a vote in a checked landing is bound to its candidate instead.
func (s stepDecisionStore) approve(ctx context.Context, expected stepDecisionSnapshot, who string, request Approval, alsoApproves []string, accepted string, adds *ChangeAdds) (stepApprovalResult, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (stepApprovalResult, error) {
		run, step, err := expected.lock(ctx, tx)
		if err != nil {
			return stepApprovalResult{}, err
		}
		// An approval judges the stage's work, so it is given after that
		// work: approving while boot and review are still to run would
		// rubber-stamp work no check had seen. A rejection may
		// come at any time.
		if request.Decision == VoteApprove {
			unfinished, err := unfinishedStageWork(ctx, tx, step)
			if err != nil {
				return stepApprovalResult{}, err
			}
			if len(unfinished) > 0 {
				return stepApprovalResult{}, &ApprovalTooEarly{Unfinished: unfinished}
			}
		}
		// Current evidence can contain another eligible person's concurrent vote.
		// The authorization snapshot pins scope/config, not a stale vote count.
		voter, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return stepApprovalResult{}, err
		}
		result, err := reworkStore(s).approveIn(ctx, tx, run, step, ApprovalVote{Approver: who, Principal: voter.Principal, Via: voter.Via, Decision: request.Decision, Comment: request.Comment, AlsoApproves: alsoApproves, Candidate: accepted, Adds: adds})
		if err != nil {
			return stepApprovalResult{}, err
		}
		updated, err := stepResult(workflowsql.New(tx.SQL()).StepByID(ctx, step.ID))
		return stepApprovalResult{updated, result}, err
	})
}
