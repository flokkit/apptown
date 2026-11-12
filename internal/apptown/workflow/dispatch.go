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
	ObservedSteps    map[ids.ID]bool
	RunID            ids.ID
	Steps            []ReadyStep
	Pending          int
	NeedsPreparation bool
	ObserveOnly      bool
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
	all, err := readSteps(ctx, tx, runID)
	if err != nil {
		return plan, err
	}
	queue, err := workflowsql.New(tx.SQL()).IsAutomatedQueueChange(ctx, run.ChangeID)
	if err != nil {
		return plan, err
	}
	// Collect every observation despite findings or errors; any other work
	// keeps the ordinary failure stop rule.
	plan.ObserveOnly = queue && len(all) > 0
	for _, step := range all {
		if step.Type != "scan" {
			plan.ObserveOnly = false
		}
	}
	plan.ObservedSteps, err = observedSteps(stage, steps)
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

// observedSteps identifies discovery checks whose verdicts inform later work.
func observedSteps(stage *schema.Stage, steps []*Step) (map[ids.ID]bool, error) {
	observations := map[ids.ID]bool{}
	if stage.Gate != nil && len(stage.Gate.Require) > 0 {
		for _, observed := range steps {
			if observed.Type != "scan" {
				continue
			}
			required := false
			for _, name := range stage.Gate.Require {
				if observed.Name == name || strings.HasPrefix(observed.Name, name+":") {
					required = true
				}
			}
			if !required {
				observations[observed.ID] = true
			}
		}
	}
	for _, caller := range steps {
		if caller.Type != "workflow/call" {
			continue
		}
		var cfg struct {
			WhenFailed []string `json:"whenFailed"`
		}
		if err := json.Unmarshal(caller.Config, &cfg); err != nil {
			return nil, err
		}
		for _, name := range cfg.WhenFailed {
			for _, observed := range steps {
				if observed.Name == name || strings.HasPrefix(observed.Name, name+":") {
					observations[observed.ID] = true
				}
			}
		}
	}
	return observations, nil
}

// dispatchSettled is the dispatcher's prerequisite rule. A conditional skip has
// no work to finish, just as a step whose subject does not apply has none.
func dispatchSettled(status StepState) bool { return status.passesGate() }

// dispatchReady applies the ordered prerequisite rule to assertions and approvals. Later steps cannot hold an earlier step.
func dispatchReady(ctx context.Context, db database.Queryer, step *Step) (bool, error) {
	steps, err := readStageSteps(ctx, db, step.RunID, step.Stage)
	if err != nil {
		return false, err
	}
	run, err := readRun(ctx, db, step.RunID)
	if err != nil {
		return false, err
	}
	spec, err := run.Definition()
	if err != nil {
		return false, err
	}
	stage, _ := spec.Stage(step.Stage)
	if stage == nil {
		return false, fmt.Errorf("stage %q is missing from the pinned definition", step.Stage)
	}
	observed, err := observedSteps(stage, steps)
	if err != nil {
		return false, err
	}
	for _, earlier := range steps {
		if earlier.ID == step.ID {
			return true, nil
		}
		if !dispatchSettled(earlier.Status) && !(observed[earlier.ID] && (earlier.Status == StatusFail || earlier.Status == StatusError)) {
			return false, nil
		}
	}
	return false, nil
}
