package workflow

import (
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// errNothingToRecheck refuses a re-run of a stage whose recorded checks are
// complete: a failed step is run again on its own, and a landing's failed
// gates all at once (RecheckLanding).
var errNothingToRecheck = fault.New(fault.Conflict, "this stage is not missing any evidence: run a failed step again with apptown run-step <step-id>, or a landing's failed gates with apptown recheck <change-id>")

// errRecheckNotRunning refuses a re-run of a stage of a run that has ended.
var errRecheckNotRunning = fault.New(fault.Conflict, "only a running run's stage can have its checks run again")

// errRecheckBusy refuses a re-run while a step of the stage is still running.
var errRecheckBusy = fault.New(fault.Conflict, "wait for the stage's running steps to finish before running its checks again")

// Rechecked names the stage whose checks run again and what it was missing.
type Rechecked struct {
	RunID   ids.ID   `json:"runId"`
	Stage   string   `json:"stage"`
	Missing []string `json:"missing"`
}

type stageRecheckReceipt struct {
	Stage   string   `json:"stage"`
	Missing []string `json:"missing"`
	By      string   `json:"by"`
}

// RecheckStage runs a stage's checks again when its gate can never pass as
// recorded: a check it requires was never recorded, so no step result can
// satisfy it. The stage is planned again from the run's pinned definition, a
// new stage.entered receipt names the checks it expects, and every planned
// step, approvals included, starts a new attempt; earlier results stay in
// their step.invalidated receipts and nothing passes on old evidence.
//
// Without it, a run whose stage opened before gates began requiring the
// stage's recorded scanner list could never advance, and every restart would
// log the same refusal while the run read "ready to advance".
func (s *Service) RecheckStage(ctx context.Context, runID ids.ID) (Rechecked, error) {
	// A run waiting on a called workflow shows that child's stage; its checks
	// are the ones to run again.
	runID, err := activeCallee(ctx, s.db, runID)
	if err != nil {
		return Rechecked{}, err
	}
	project, actor, err := s.runScope(ctx, runID, actionRetryAttempt)
	if err != nil {
		return Rechecked{}, err
	}
	out, err := database.Transact(ctx, s.db, func(tx *database.Tx) (Rechecked, error) {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, project); err != nil {
			return Rechecked{}, err
		}
		run, err := runRecord(q.RunForUpdate(ctx, runID))
		if err != nil {
			return Rechecked{}, database.Missing(err)
		}
		if run.ProjectID != project {
			return Rechecked{}, ErrRunMoved
		}
		if run.Status != RunRunning {
			return Rechecked{}, errRecheckNotRunning
		}
		_, stage, err := run.currentStage()
		if err != nil {
			return Rechecked{}, err
		}
		steps, err := stepRecords(q.StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: run.CurrentStage}))
		if err != nil {
			return Rechecked{}, err
		}
		for _, step := range steps {
			if step.Status == StatusRunning {
				return Rechecked{}, errRecheckBusy
			}
		}
		recorded, err := readStageChecks(ctx, tx, run.ID, run.CurrentStage)
		if err != nil {
			return Rechecked{}, err
		}
		verdict, err := run.Advance(steps, recorded, s.placement)
		if err != nil {
			return Rechecked{}, err
		}
		if len(verdict.Missing) == 0 {
			return Rechecked{}, errNothingToRecheck
		}
		var scanners []Scanner
		if s.stages.scanners != nil {
			if scanners, err = s.stages.scanners.SnapshotIn(ctx, tx); err != nil {
				return Rechecked{}, err
			}
		}
		plan, err := planStage(stage, scanners)
		if err != nil {
			return Rechecked{}, err
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "stage.rechecked", stageRecheckReceipt{run.CurrentStage, verdict.Missing, actor.Receipt}, s.now()); err != nil {
			return Rechecked{}, err
		}
		checks := make([]Check, 0, len(plan))
		for _, p := range plan {
			checks = append(checks, p.check)
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "stage.entered", stageEnteredReceipt{run.CurrentStage, checks}, s.now()); err != nil {
			return Rechecked{}, err
		}
		byName := map[string]*Step{}
		for _, step := range steps {
			byName[step.Name] = step
		}
		for _, p := range plan {
			step := byName[p.check.Name]
			if step == nil {
				if _, err := q.InsertStageStep(ctx, workflowsql.InsertStageStepParams{ID: ids.New(s.now()), RunID: run.ID, Stage: run.CurrentStage, Name: p.check.Name, Type: p.check.Type, ConfigJson: json.RawMessage(p.configJSON), Status: StatusPending.String()}); err != nil {
					return Rechecked{}, err
				}
				continue
			}
			// A pending step starts again too when it holds evidence: an
			// approval short of its quorum keeps votes that were cast on the
			// old evidence.
			if step.Status == StatusPending && evidenceEmpty(step.Evidence) {
				continue
			}
			if step.Status != StatusPending {
				if _, err := step.Status.Transition(MoveStepReset, StatusPending); err != nil {
					return Rechecked{}, err
				}
			}
			if err := recordReceiptIn(ctx, tx, run.ID, "step.invalidated", retryInvalidation{step.Name, step.ID, "stage checks run again", step.Status, step.Evidence, step.OverrideBy}, s.now()); err != nil {
				return Rechecked{}, err
			}
			if err := requireAttemptWrite(q.InvalidateStep(ctx, workflowsql.InvalidateStepParams{ID: step.ID, Attempt: step.Attempt})); err != nil {
				return Rechecked{}, err
			}
		}
		if err := invalidateRunDecisionIn(ctx, tx, run, s.now()); err != nil {
			return Rechecked{}, err
		}
		return Rechecked{run.ID, run.CurrentStage, verdict.Missing}, nil
	})
	if err != nil {
		return Rechecked{}, err
	}
	s.DispatchCommittedRetry(runID)
	return out, nil
}

func evidenceEmpty(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return len(raw) == 0 || json.Unmarshal(raw, &fields) == nil && len(fields) == 0
}

// activeCallee follows a run's running workflow call to the child it waits
// on, as deep as calls go; a run with no running call is itself.
func activeCallee(ctx context.Context, q database.Queryer, runID ids.ID) (ids.ID, error) {
	graph, err := readWorkflowActivity(ctx, q, []ids.ID{runID}, Placement{})
	if err != nil {
		return ids.Nil, err
	}
	seen := map[ids.ID]bool{}
	for id := runID; ; {
		node := graph[id]
		if node == nil || seen[id] {
			return id, nil
		}
		seen[id] = true
		next := ids.ID(ids.Nil)
		for _, step := range node.steps {
			if step.kind != "workflow/call" || step.state != StatusRunning || len(step.evidence) == 0 {
				continue
			}
			var call struct {
				ChildRunID ids.ID `json:"childRunId"`
			}
			if json.Unmarshal(step.evidence, &call) == nil && graph[call.ChildRunID] != nil && graph[call.ChildRunID].parentID == id && graph[call.ChildRunID].state == RunRunning {
				next = call.ChildRunID
			}
		}
		if next.IsZero() {
			return id, nil
		}
		id = next
	}
}
