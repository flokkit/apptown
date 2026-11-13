package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

// standingEvidence reads the captured procedure rather than just its current
// stage. Observing steps outside the root's gate retain their own verdicts.
// A call's complete procedure is evidence, including checks outside its gate.
func standingEvidence(ctx context.Context, q database.Queryer, run *Run, called bool, depth int) (string, error) {
	if depth > maxCallDepth {
		return "the call tree exceeds the supported depth", nil
	}
	spec, err := run.Definition()
	if err != nil {
		return "", err
	}
	all, err := readSteps(ctx, q, run.ID)
	if err != nil {
		return "", err
	}
	for _, stage := range spec.Stages {
		checks, err := readStageChecks(ctx, q, run.ID, stage.Name)
		if err != nil {
			return "", err
		}
		var at []*Step
		for _, step := range all {
			if step.Stage == stage.Name {
				at = append(at, step)
			}
		}
		gate, err := evaluateGate(&stage, checks, at)
		if err != nil {
			return "", err
		}
		if len(gate.Missing) > 0 {
			return "missing standing evidence: " + strings.Join(gate.Missing, ", "), nil
		}
		for _, step := range at {
			required := slices.Contains(gate.Required, step.Name)
			for _, check := range checks {
				if check.Name == step.Name && slices.Contains(gate.Required, check.Declaration) {
					required = true
				}
			}
			if !called && !required && step.Type != "workflow/call" {
				continue
			}

			// Approval and merge-result steps are decisions/effects, not
			// evidence that must exist before the same effect can be claimed.
			if step.Type == "approval" || step.Type == "scm/merged" {
				continue
			}
			var control struct {
				Reason       string `json:"reason"`
				ChildRunID   ids.ID `json:"childRunId"`
				RepairRounds int    `json:"repairRounds"`
			}
			if step.Type == "agent/pi" && json.Unmarshal(step.Config, &control) == nil && control.RepairRounds > 0 && (step.Status == StatusSkipped || step.Status == StatusNotApplicable) && step.OverrideBy == "" {
				continue
			}
			if step.Type == "workflow/call" {
				if err := json.Unmarshal(step.Evidence, &control); err != nil {
					return step.Name + " has unreadable call evidence", nil
				}
				if strings.TrimSpace(control.Reason) != "" && step.Status == StatusNotApplicable && step.OverrideBy == "" {
					continue
				}
				if control.ChildRunID.IsZero() {
					return step.Name + " has no called run", nil
				}
				child, err := readRun(ctx, q, control.ChildRunID)
				if err != nil {
					return "", err
				}
				if child.ProjectID != run.ProjectID || child.ParentRunID != run.ID || child.ParentStepID != step.ID || child.ParentStepAttempt != step.Attempt || child.Status != RunDone {
					return step.Name + " does not name its completed call", nil
				}
				why, err := standingEvidence(ctx, q, child, true, depth+1)
				if err != nil || why != "" {
					return why, err
				}
			}
			if why := passedOutright([]*Step{step}); why != "" {
				return why, nil
			}
		}
		// A landing has stages after its approving stage. They have not executed.
		if stage.Name == run.CurrentStage {
			break
		}
	}
	return "", nil
}

// standingConsidered fences one decision per retry revision, independently of
// the River job's deduplication and retention.
func standingConsidered(ctx context.Context, q database.Queryer, run, step ids.ID, revision int64) (bool, error) {
	return workflowsql.New(q.SQL()).StandingConsidered(ctx, workflowsql.StandingConsideredParams{RunID: run, Step: step.String(), Revision: revision})
}

// UpdateTargetsIn reads the findings the current call tree actually acted on.
// Fresh standing classification must judge FixedIn for those same targets,
// even when the root is the observing scan and the fix ran in a child.
func (s Source) UpdateTargetsIn(ctx context.Context, q database.Queryer) ([]check.Finding, error) {
	run, err := readRun(ctx, q, s.RunID)
	if err != nil {
		return nil, err
	}
	if run.ProjectID != s.ProjectID {
		return nil, ErrRunMoved
	}
	return updateTargetsIn(ctx, q, run, 0)
}
func updateTargetsIn(ctx context.Context, q database.Queryer, run *Run, depth int) ([]check.Finding, error) {
	if depth > maxCallDepth {
		return nil, fmt.Errorf("call tree exceeds supported depth")
	}
	targets, err := fix.Targets(run.InputsJSON)
	if err != nil {
		return nil, err
	}
	steps, err := readSteps(ctx, q, run.ID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		if step.Type != "workflow/call" || step.Status != StatusPass {
			continue
		}
		var call struct {
			ChildRunID ids.ID `json:"childRunId"`
		}
		if err := json.Unmarshal(step.Evidence, &call); err != nil {
			return nil, err
		}
		child, err := readRun(ctx, q, call.ChildRunID)
		if err != nil {
			return nil, err
		}
		if child.ParentRunID != run.ID || child.ParentStepID != step.ID || child.ParentStepAttempt != step.Attempt || child.ProjectID != run.ProjectID {
			return nil, ErrRunMoved
		}
		more, err := updateTargetsIn(ctx, q, child, depth+1)
		if err != nil {
			return nil, err
		}
		targets = append(targets, more...)
	}
	return targets, nil
}
