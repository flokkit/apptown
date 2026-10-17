package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type CompletionEffect uint8

const (
	CompletionCleanup CompletionEffect = iota + 1
	CompletionNarrative
	CompletionAdopt
	CompletionLand
	CompletionParent
)

func (e CompletionEffect) String() string {
	switch e {
	case CompletionCleanup:
		return "cleanup"
	case CompletionNarrative:
		return "narrative"
	case CompletionAdopt:
		return "adopt"
	case CompletionLand:
		return "land"
	case CompletionParent:
		return "parent"
	default:
		return "invalid-completion-effect"
	}
}

type CompletedRun struct {
	RunID, ProjectID, ChangeID ids.ID
	Release                    bool
	// ApprovedBy names who approved the run's approvals, as their votes
	// recorded them: the people answerable for what it releases. Empty when
	// no approval passed by a vote.
	ApprovedBy string
	Effects    []CompletionEffect
}
type ReleaseReceipt struct {
	ID     ids.ID `json:"release"`
	Number int64  `json:"number"`
}
type CompletionRecorder interface {
	RecordCompletionIn(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error)
	RetryCompletionIn(context.Context, *database.Tx, ids.ID) (bool, error)
}

func (w advanceStore) retryCompletion(ctx context.Context, expected *Run) (bool, error) {
	if w.completion == nil {
		return false, fmt.Errorf("completion effects are unavailable")
	}
	if expected == nil || expected.Status != RunDone {
		return false, ErrRunMoved
	}
	snapshot := *expected
	return database.Transact(ctx, w.db, func(tx *database.Tx) (bool, error) {
		run, err := lockRunSnapshotIn(ctx, tx, &snapshot)
		if err != nil {
			return false, err
		}
		return w.completion.RetryCompletionIn(ctx, tx, run.ID)
	})
}
func (w advanceStore) completedParent(ctx context.Context, child *Run) (*Run, error) {
	if child.ParentRunID.IsZero() || child.ParentStepID.IsZero() {
		return nil, nil
	}
	row, err := workflowsql.New(w.db.SQL()).CompletedParent(ctx, workflowsql.CompletedParentParams{
		ParentRunID: child.ParentRunID, ParentStepID: child.ParentStepID, ProjectID: child.ProjectID,
		ParentStepAttempt: child.ParentStepAttempt, ChildRunID: child.ID,
	})
	if err != nil {
		return nil, err
	}
	parent, err := runRecord(row.Run, nil)
	if err != nil {
		return nil, err
	}
	if !row.CurrentCall || parent.Status != RunRunning {
		return nil, nil
	}
	return parent, nil
}

func (w advanceStore) complete(ctx context.Context, run *Run) error {
	if w.completion == nil {
		return fmt.Errorf("completion effects are unavailable")
	}
	return w.db.InTx(ctx, func(tx *database.Tx) error {
		spec, err := settleAdvanceIn(ctx, tx, run, stageSuccessor{Completing: true}, w.placement, w.now())
		if err != nil {
			return err
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "run.done", struct{}{}, w.now()); err != nil {
			return err
		}
		completed := CompletedRun{RunID: run.ID, ProjectID: run.ProjectID, ChangeID: run.ChangeID, Effects: []CompletionEffect{CompletionCleanup}}
		if run.ParentRunID.IsZero() && !run.ChangeID.IsZero() {
			completed.Effects = append(completed.Effects, CompletionNarrative)
			// A landing ships its change. So does a record run (research, a
			// specification): accepting it closes the change, and with
			// nothing committed there is nothing to land.
			if spec.Role != "working" {
				change, err := TransitionChangeIn(ctx, tx, run.ProjectID, run.ChangeID, ChangeMove{Action: ShipChange}, w.now())
				if err != nil {
					return fmt.Errorf("shipping change %d: %w", run.ChangeID, err)
				}
				if change.Type == "uplift" {
					completed.Effects = append(completed.Effects, CompletionAdopt)
				}
				completed.Release, err = workflowsql.New(tx.SQL()).CompletionHasProductionDeploy(ctx, workflowsql.CompletionHasProductionDeployParams{RunID: run.ID, ProjectID: run.ProjectID, Status: StatusPass.String()})
				if err != nil {
					return err
				}
				if completed.Release {
					if completed.ApprovedBy, err = approvedByIn(ctx, tx, run); err != nil {
						return err
					}
				}
			} else {
				if err := recordReceiptIn(ctx, tx, run.ID, "landing.queued", struct {
					Change ids.ID `json:"change"`
				}{run.ChangeID}, w.now()); err != nil {
					return err
				}
				if _, err := workflowsql.New(tx.SQL()).EnsureLanding(ctx, run.ID); err != nil {
					return err
				}
				completed.Effects = append(completed.Effects, CompletionLand)
			}
		}
		if !run.ParentRunID.IsZero() && !run.ParentStepID.IsZero() {
			if err := recordCompletedChild(ctx, tx, run, w.now()); err != nil {
				return err
			}
			completed.Effects = append(completed.Effects, CompletionParent)
		}
		receipt, err := w.completion.RecordCompletionIn(ctx, tx, completed)
		if err != nil {
			return err
		}
		if completed.Release {
			if receipt.ID.IsZero() || receipt.Number <= 0 {
				return fmt.Errorf("required completion release was not recorded")
			}
			if err := recordReceiptIn(ctx, tx, run.ID, "release.created", receipt, w.now()); err != nil {
				return err
			}
		}
		return nil
	})
}
func recordCompletedChild(ctx context.Context, tx *database.Tx, child *Run, at time.Time) error {
	call, err := workflowsql.New(tx.SQL()).ParentCallForUpdate(ctx, workflowsql.ParentCallForUpdateParams{ParentRunID: child.ParentRunID, ParentStepID: child.ParentStepID})
	if err != nil {
		return err
	}
	if call.ParentProjectID != child.ProjectID {
		return ErrRunMoved
	}
	parentState, err := ParseRunState(call.ParentStatus)
	if err != nil {
		return err
	}
	callState, err := ParseStepState(call.Status)
	if err != nil {
		return err
	}
	if parentState != RunRunning || call.ParentStage != call.Stage || callState != StatusRunning || call.Attempt != child.ParentStepAttempt {
		return nil
	}
	if _, err := callState.Transition(MoveStepFinish, StatusPass); err != nil {
		return err
	}
	ev, err := retryConfiguration(call.EvidenceJson)
	if err != nil {
		return err
	}
	var named ids.ID
	if json.Unmarshal(ev["childRunId"], &named) != nil || named != child.ID {
		return nil
	}
	ev["childRunId"], _ = json.Marshal(child.ID)
	setRetryField(ev, "workflow", child.WorkflowName)
	setRetryField(ev, "outcome", RunDone.String())
	ev["workflowVersion"] = json.RawMessage(strconv.Itoa(child.WorkflowVersion))
	if raw := ev["startedAtMs"]; len(raw) > 0 && ((raw[0] >= '0' && raw[0] <= '9') || raw[0] == '-') {
		ms, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("child call start time: %w", err)
		}
		ev["durationMs"] = json.RawMessage(strconv.FormatInt(at.UnixMilli()-ms, 10))
		delete(ev, "startedAtMs")
	}
	q := workflowsql.New(tx.SQL())
	artifacts, err := q.CompletionChildArtifactCount(ctx, child.ID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	got, writeErr := q.CompletionCopyChildArtifacts(ctx, workflowsql.CompletionCopyChildArtifactsParams{NewIds: mintIDs(artifacts, at), ParentRunID: child.ParentRunID, CreatedAt: at.UTC().Truncate(time.Second), ChildRunID: child.ID})
	if err := continuationCount(artifacts, got, writeErr); err != nil {
		return err
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := requireAttemptWrite(workflowsql.New(tx.SQL()).FinishChildCall(ctx, workflowsql.FinishChildCallParams{At: at, EvidenceJson: body, ID: child.ParentStepID, Attempt: call.Attempt})); err != nil {
		return err
	}
	return recordReceiptIn(ctx, tx, child.ParentRunID, "step.finished", childFinishedReceipt{call.Name, StatusPass, child.ID, call.Attempt}, at)
}

type childFinishedReceipt struct {
	Step       string    `json:"step"`
	Status     StepState `json:"status"`
	ChildRunID ids.ID    `json:"childRunId"`
	Attempt    int64     `json:"attempt"`
}

// approvedByIn names the people whose votes passed the run's approvals, in
// the order they voted, each once.
func approvedByIn(ctx context.Context, tx *database.Tx, run *Run) (string, error) {
	steps, err := ReadScopedStepsIn(ctx, tx, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return "", err
	}
	var names []string
	for _, step := range steps {
		if step.Type != "approval" || step.Status != StatusPass || step.OverrideBy != "" {
			continue
		}
		evidence, err := ReadApprovalEvidence(step.Evidence)
		if err != nil {
			return "", err
		}
		for _, vote := range evidence.Decisions {
			if vote.Decision == VoteApprove && vote.Approver != "" && !slices.Contains(names, vote.Approver) {
				names = append(names, vote.Approver)
			}
		}
	}
	return strings.Join(names, ", "), nil
}
