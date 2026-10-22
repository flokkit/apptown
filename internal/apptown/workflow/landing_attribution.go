package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// landingAttributionIn reads, in the transaction that admits run's merge,
// what the record says about it for GitLab readers (scm.Attribution): who
// opened the change and what it is for, each approving vote of a deciding
// round in the run or a run that called it (the facts assentEvidenceIn
// accepts), the landing authority recorded when the run was admitted,
// overrides, and the run's candidate checks with the result each recorded.
// A failed read is an error, never an empty attribution.
//
// It states what was recorded, not more: an approval carries the approvers
// its step declared, which is not the authority the vote was checked against
// (requireApprover lets an instance root through without matching it, and an
// empty list means the project's defaults at the time); the landing authority
// was held when the run was admitted, not necessarily now.
func landingAttributionIn(ctx context.Context, q database.Queryer, run *Run, source MergeSource) (*scm.Attribution, error) {
	a := &scm.Attribution{Workflow: fmt.Sprintf("%s@v%d", run.WorkflowName, run.WorkflowVersion), RunID: run.ID, RunNumber: run.Number, Candidate: source.Landing.Candidate}
	if !source.ChangeID.IsZero() {
		change, err := readChange(ctx, q, source.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("reading the change a merge attributes: %w", database.Missing(err))
		}
		a.ChangeID, a.ChangeNumber, a.Title, a.Body, a.OpenedBy = change.ID, change.Number, change.Title, change.Body, change.OpenedBy
	}
	spec, err := run.Definition()
	if err != nil {
		return nil, err
	}
	reached := map[string]bool{}
	for _, stage := range spec.Stages {
		reached[stage.Name] = true
		if stage.Name == run.CurrentStage {
			break
		}
	}
	queries := workflowsql.New(q.SQL())
	for id, depth := run.ID, 0; !id.IsZero() && depth <= maxCallDepth+1; depth++ {
		facts, err := queries.RunLandingAuthority(ctx, id)
		if err != nil {
			return nil, database.Missing(err)
		}
		if eval, ok := evalOfAuthority(facts.LandingAuthority); ok && a.EvalAuthority == "" {
			a.EvalAuthority = fmt.Sprint(eval)
		} else if !ok && facts.LandingAuthority != "" && a.LandingAuthority == "" {
			a.LandingAuthority = facts.LandingAuthority
		}
		steps, err := readSteps(ctx, q, id)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			// The landing run's later stages have not run; a caller's steps
			// have all been reached by the time it called.
			if id == run.ID && !reached[step.Stage] {
				continue
			}
			if err := attributeStep(a, step, id == run.ID); err != nil {
				return nil, err
			}
		}
		id = facts.ParentRunID
	}
	return a, nil
}

// attributeStep adds one step's recorded facts. Checks are the landing run's
// candidate gates (the steps a merge requires to name its candidate).
func attributeStep(a *scm.Attribution, step *Step, landing bool) error {
	if step.OverrideBy != "" {
		a.Overrides = append(a.Overrides, scm.AttributedOverride{Step: step.Name, By: step.OverrideBy, Reason: step.OverrideReason})
	}
	if landing && slices.Contains(candidateGates, step.Type) {
		a.Checks = append(a.Checks, scm.AttributedCheck{Step: step.Name, StepID: step.ID, Attempt: step.Attempt, Result: checkResult(step)})
	}
	if step.Type != "approval" || step.Status != StatusPass || step.OverrideBy != "" {
		return nil
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return fmt.Errorf("step %s: %w", step.Name, err)
	}
	if evidence.EvalAuthorised != "" {
		a.Approvals = append(a.Approvals, scm.AttributedApproval{Step: step.Name, Eval: evidence.EvalAuthorised})
		return nil
	}
	cfg, err := ReadApprovalConfig(step.Config)
	if err != nil {
		return fmt.Errorf("step %s: %w", step.Name, err)
	}
	means := evidence.Means
	if means == "" {
		means = cfg.Means
	}
	votes := evidence.Decisions
	if len(votes) == 0 && evidence.Approver != "" {
		// A decision recorded before votes were kept separately.
		votes = []ApprovalVote{{Approver: evidence.Approver, Decision: evidence.Decision, At: evidence.At, Candidate: evidence.Candidate}}
	}
	for _, vote := range votes {
		if vote.Decision != VoteApprove {
			continue
		}
		a.Approvals = append(a.Approvals, scm.AttributedApproval{Step: step.Name, By: vote.Approver, At: vote.At, Means: means,
			Declared: slices.Clone(cfg.Approvers), AlsoApproves: slices.Clone(vote.AlsoApproves), Candidate: vote.Candidate})
	}
	return nil
}

// checkResult is a check's recorded outcome in words. Skipped lets a gate
// through but is not a pass, and an override is not a pass either.
func checkResult(step *Step) string {
	if step.OverrideBy != "" {
		return "overridden"
	}
	switch step.Status {
	case StatusPass:
		return "passed"
	case StatusSkipped:
		return "skipped"
	case StatusFail:
		return "failed"
	case StatusNeedsHuman:
		return "waiting for a person"
	case StatusPending:
		return "not run"
	default:
		return step.Status.String()
	}
}
