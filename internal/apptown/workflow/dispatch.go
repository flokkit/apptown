package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// dispatchPlans observes already-admitted Work; returned plans do not grant an
// execution claim. The runner registry is copied once and never exposed.
type dispatchPlans struct {
	db        *database.DB
	automatic []string
	clocked
}

func newDispatchPlans(db *database.DB, automatic []string) dispatchPlans {
	return dispatchPlans{db: db, automatic: slices.Clone(automatic)}
}

type DispatchPlan struct {
	RunID            ids.ID
	Steps            []ReadyStep
	Pending          int
	NeedsPreparation bool
	AdvanceEmpty     bool
}
type dispatchState struct {
	Status           StepState
	Orphaned, Active bool
}

func (s dispatchState) runnable() bool { return s.Status == StatusPending || s.Orphaned }
func (w dispatchPlans) State(ctx context.Context, runID, stepID ids.ID) (dispatchState, error) {
	var state dispatchState
	row, err := workflowsql.New(w.db.SQL()).DispatchStepState(ctx, workflowsql.DispatchStepStateParams{RunID: runID, ID: stepID})
	if err != nil {
		return state, err
	}
	state.Status, err = parseStepState(row.Status)
	state.Orphaned, state.Active = row.Orphaned.Bool, row.Active.Bool
	return state, err
}

// RestartCandidates preserves the existing explicit restart policy. Ordinary
// runner failures are never replayed by this query; they require an explicit rerun.
func (w dispatchPlans) RestartCandidates(ctx context.Context) ([]ids.ID, error) {
	rows, err := workflowsql.New(w.db.SQL()).DispatchRestartCandidates(ctx, w.automatic)
	if err == nil && rows == nil {
		rows = []ids.ID{}
	}
	return rows, err
}

// RunOf is the run a step belongs to.
func (w dispatchPlans) RunOf(ctx context.Context, stepID ids.ID) (ids.ID, error) {
	step, err := readStep(ctx, w.db, stepID)
	if err != nil {
		return ids.Nil, err
	}
	return step.RunID, nil
}

func (w dispatchPlans) Plan(ctx context.Context, runID ids.ID) (DispatchPlan, error) {
	plan := DispatchPlan{RunID: runID}
	tx, err := w.db.ReadSnapshot(ctx)
	if err != nil {
		return plan, err
	}
	defer func() { _ = tx.Rollback() }()
	run, err := readRun(ctx, tx, runID)
	if err != nil {
		return plan, err
	}
	if run.Status != RunRunning {
		return plan, tx.Commit()
	}
	plan.AdvanceEmpty = !run.ParentRunID.IsZero()
	plan.NeedsPreparation, err = runNeedsPreparation(ctx, tx, runID)
	if err != nil {
		return plan, err
	}
	spec, err := run.Definition()
	if err != nil {
		return plan, err
	}
	stage, _ := spec.Stage(run.CurrentStage)
	if stage == nil {
		return plan, fmt.Errorf("stage %q is missing from the pinned definition", run.CurrentStage)
	}
	steps, err := readStageSteps(ctx, tx, runID, run.CurrentStage)
	if err != nil {
		return plan, err
	}
	for _, step := range steps {
		if step.Stage != run.CurrentStage || !slices.Contains(w.automatic, step.Type) {
			continue
		}
		plan.Steps = append(plan.Steps, ReadyStep{step.ID, step.Type, entryStep(stage, step), step.Name})
		if step.Status == StatusPending || restartOrphan(step) {
			plan.Pending++
		}
	}
	return plan, tx.Commit()
}
func entryStep(stage *schema.Stage, step *Step) bool {
	for _, s := range stage.OnEnter {
		if s.Name == step.Name && s.Uses == step.Type {
			return true
		}
	}
	// An explicitly named gate takes precedence over a scan point prefix.
	for _, s := range stage.Steps {
		if s.Name == step.Name {
			return false
		}
	}
	for _, s := range stage.OnEnter {
		if s.Uses == "scan" && step.Type == "scan" {
			if on, ok := s.With["on"].(string); ok && on != "" && strings.HasPrefix(step.Name, s.Name+":") {
				return true
			}
		}
	}
	return false
}

// Keep the persisted reason stable: rechecks also recognize existing skips.
func skipReason(after string) string { return "not run: “" + after + "” did not pass" }

// A skip and its receipt commit together. Only an unstarted or restart-orphaned
// step in a still-active stage can be skipped; a finished outcome is preserved.
func (w dispatchPlans) Skip(ctx context.Context, runID, stepID ids.ID, after string) error {
	return w.db.InTx(ctx, func(tx *database.Tx) error {
		run, current, err := lockStep(ctx, tx, stepID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if run.ID != runID || run.Status != RunRunning || run.CurrentStage != current.Stage || current.OverrideBy != "" || !slices.Contains(w.automatic, current.Type) {
			return nil
		}
		if current.Status != StatusPending && !restartOrphan(current) {
			return nil
		}
		if _, err := current.Status.transition(moveStepSkip, StatusSkipped); err != nil {
			return err
		}
		evidence, err := json.Marshal(struct {
			Reason string `json:"reason"`
		}{skipReason(after)})
		if err != nil {
			return err
		}
		count, err := workflowsql.New(tx.SQL()).DispatchSkip(ctx, workflowsql.DispatchSkipParams{At: w.now(), EvidenceJson: evidence, ID: stepID, RunID: runID})
		if err := requireAttemptWrite(count, err); err != nil {
			return err
		}
		return recordReceiptIn(ctx, tx, runID, "step.skipped", struct {
			Step  string `json:"step"`
			After string `json:"after"`
		}{current.Name, after}, w.now())
	})
}

// Prepare the restart retry before invoking its runner. Agents continue their
// saved session; clearing the orphan verdict and recording the resume are one
// write. If this fails, no external work starts.
func (w dispatchPlans) Resume(ctx context.Context, runID, stepID ids.ID) (bool, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := lockStep(ctx, tx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if run.ID != runID || run.Status != RunRunning || run.CurrentStage != current.Stage || current.OverrideBy != "" || !slices.Contains(w.automatic, current.Type) || !restartOrphan(current) {
		return false, nil
	}
	if _, err := current.Status.transition(moveStepReset, StatusPending); err != nil {
		return false, err
	}
	count, err := workflowsql.New(tx.SQL()).DispatchResume(ctx, workflowsql.DispatchResumeParams{ID: stepID, RunID: runID})
	if err := requireAttemptWrite(count, err); err != nil {
		return false, err
	}
	reason := "AppTown restarted; the step resumes"
	if current.Type == "agent/pi" {
		reason = "AppTown restarted; the agent continues its session"
	}
	if err := recordReceiptIn(ctx, tx, runID, "step.resumed", struct {
		Step   ids.ID `json:"step"`
		Reason string `json:"reason"`
	}{stepID, reason}, w.now()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Recovery is limited to the explicit restart verdict, never arbitrary failures.
func restartOrphan(step *Step) bool {
	var evidence struct {
		Reason string `json:"reason"`
	}
	return step.Status == StatusError && json.Unmarshal(step.Evidence, &evidence) == nil && evidence.Reason == "orphaned by apptownd restart"
}
