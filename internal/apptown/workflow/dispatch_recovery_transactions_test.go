package workflow

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type recordedScanners []Scanner

func (s recordedScanners) SnapshotIn(context.Context, database.Queryer) ([]Scanner, error) {
	return append([]Scanner(nil), s...), nil
}

type recoveryStageEffects struct{}

func (recoveryStageEffects) Prepare(ids.ID) error { return nil }
func (recoveryStageEffects) Advance(ids.ID)       {}

type recoveryAdvancement struct {
	advance func(ids.ID)
}

func (recoveryAdvancement) Prepare(ids.ID) error { return nil }
func (a recoveryAdvancement) Advance(id ids.ID)  { a.advance(id) }

func persistedRecoveryFixture(t *testing.T, stage schema.Stage, scanners []Scanner) (*database.DB, ids.ID, []ReadyStep) {
	t.Helper()
	db := testDB(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "dispatch", Name: "Dispatch"})
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Role: "working", Stages: []schema.Stage{stage}}
	// Recovery starts with a precisely pinned historical record. Direct
	// persistence here is the owner's representation guarantee.
	runID := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: projectID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: stage.Name, DefinitionJson: []byte(mustJSON(spec)), Status: RunRunning.String()}).ID
	storedReadyStart(t, db, runID)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ready, err := NewStages(recordedScanners(scanners), func(uses string) bool {
		return uses == "script" || uses == "scan" || uses == "deploy" || strings.HasPrefix(uses, "agent/")
	}).EnterIn(context.Background(), tx, runID, &spec, stage.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, runID, ready
}

func TestWorkflowRecoveryUsesPinnedSequenceAndRecordedScanners(t *testing.T) {
	t.Parallel()
	stage := schema.Stage{Name: "work", OnEnter: []schema.Step{
		{Name: "prepare", Uses: "script"},
		{Name: "image", Uses: "scan", With: map[string]any{"on": "image"}},
		{Name: "deploy", Uses: "deploy"},
	}, Steps: []schema.Step{{Name: "source", Uses: "scan", With: map[string]any{"on": "source"}}}}
	db, id, ready := persistedRecoveryFixture(t, stage, []Scanner{{Name: "old-image", Target: "image"}, {Name: "source-a", Target: "source"}, {Name: "source-b", Target: "source"}})
	transitionStoredStepForTest(t, db, ready[0].ID, StatusPass)
	transitionStoredStepForTest(t, db, ready[1].ID, StatusFail)
	plans := newDispatchPlans(db, []string{"deploy", "scan", "script"})
	plan, err := plans.Plan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Steps, ready) || plan.Pending != 3 {
		t.Fatalf("recovery plan=%+v, original=%+v", plan, ready)
	}
	var calls []ids.ID
	d := newDispatcher(db, dispatcherConfig{life: context.Background(), runners: map[string]func(ids.ID){
		"scan": func(stepID ids.ID) {
			step, err := readStep(context.Background(), db, stepID)
			if err != nil {
				t.Error(err)
				return
			}
			if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: step.Evidence, ID: stepID, Attempt: step.Attempt, ExpectedStatus: step.Status.String()})); err != nil {
				t.Error(err)
			}
		},
		"deploy": func(stepID ids.ID) { calls = append(calls, stepID) },
		"script": func(stepID ids.ID) { calls = append(calls, stepID) },
	}, stages: recoveryStageEffects{}, report: func(err error) { t.Error(err) }})
	d.restartPending(nil)
	d.wait()
	if len(calls) != 0 {
		t.Fatalf("replayed completed work or deployed past failure: %v", calls)
	}
	state, err := plans.State(context.Background(), id, ready[2].ID)
	if err != nil || state.Status != StatusSkipped {
		t.Fatalf("deploy=%+v, %v", state, err)
	}
	for _, step := range ready[3:] {
		state, err := plans.State(context.Background(), id, step.ID)
		if err != nil || state.Status != StatusPass {
			t.Fatalf("independent scan=%+v, %v", state, err)
		}
	}
	if reason, _ := jsonText(t, storedStep(t, db, ready[2].ID).Evidence, "reason"); !strings.Contains(reason, "image:old-image") {
		t.Fatalf("skip lost its prerequisite: %q", reason)
	}
}

func TestWorkflowRecoveryAdvancesFinishedChecksThroughTheGate(t *testing.T) {
	t.Parallel()
	db, id, ready := persistedRecoveryFixture(t, schema.Stage{Name: "work",
		OnEnter: []schema.Step{{Name: "check", Uses: "script"}},
		Steps:   []schema.Step{{Name: "accept", Uses: "approval"}},
	}, nil)
	transitionStoredStepForTest(t, db, ready[0].ID, StatusPass)

	checks := advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}
	advancer := newAdvancer(db, advanceRuntime{checks: checks, completion: completionRecorderFunc(noCompletionEffects)})
	attempts := 0
	stages := recoveryAdvancement{advance: func(runID ids.ID) {
		attempts++
		if _, err := advancer.advance(context.Background(), runID, "system"); err != nil {
			t.Error(err)
		}
	}}
	dispatcher := newDispatcher(db, dispatcherConfig{life: context.Background(),
		runners: map[string]func(ids.ID){"script": func(ids.ID) { t.Error("finished check was replayed") }},
		stages:  stages,
		report:  func(err error) { t.Error(err) },
	})

	dispatcher.restartPending(nil)
	dispatcher.wait()
	run, err := readRun(context.Background(), db, id)
	if err != nil || run.Status != RunRunning || attempts != 1 {
		t.Fatalf("approval gate bypassed: run=%+v attempts=%d err=%v", run, attempts, err)
	}

	// Acceptance may commit immediately before a restart, while its automatic
	// advancement has not. Recovery must finish that run without replaying work.
	steps, err := readSteps(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	var approvalID ids.ID
	for _, step := range steps {
		if step.Type == "approval" {
			approvalID = step.ID
			break
		}
	}
	if approvalID.IsZero() {
		t.Fatal("approval step not stored")
	}
	transitionStoredStepForTest(t, db, approvalID, StatusPass)
	dispatcher.restartPending(nil)
	dispatcher.wait()
	run, err = readRun(context.Background(), db, id)
	if err != nil || run.Status != RunDone || attempts != 2 {
		t.Fatalf("ready stage did not finish: run=%+v attempts=%d err=%v", run, attempts, err)
	}

	dispatcher.restartPending(nil)
	dispatcher.wait()
	if attempts != 2 {
		t.Fatalf("completed run recovered again: attempts=%d", attempts)
	}
}

func TestWorkflowRecoveryRecordsAgentContinuationBeforeRunning(t *testing.T) {
	t.Parallel()
	for _, failReceipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "receipt failure"}[failReceipt], func(t *testing.T) {
			db, id, ready := persistedRecoveryFixture(t, schema.Stage{Name: "work", OnEnter: []schema.Step{{Name: "agent", Uses: "agent/pi", With: map[string]any{"model": "pinned"}}}}, nil)
			step := ready[0]
			changed, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusError.String(), EvidenceJson: []byte(`{"reason":"orphaned by apptownd restart"}`), ID: step.ID, Attempt: 0, ExpectedStatus: StatusPending.String()})
			if err != nil || changed != 1 {
				t.Fatalf("store orphaned step: changed=%d %v", changed, err)
			}
			if failReceipt {
				completionExec(t, db, `ALTER TABLE run_events ADD CONSTRAINT reject_resume CHECK (kind!='step.resumed')`)
			}
			called, reported := false, false
			d := newDispatcher(db, dispatcherConfig{life: context.Background(), runners: map[string]func(ids.ID){"agent/pi": func(stepID ids.ID) {
				called = true
				prepared, err := readStep(context.Background(), db, stepID)
				if err != nil {
					t.Error(err)
					return
				}
				if prepared.Status != StatusPending || !jsonMatches(t, prepared.Config, "retryMode=continue", "model=pinned") || prepared.FinishedAt.Valid {
					t.Errorf("resume preparation: %s %s finished=%v", prepared.Status, prepared.Config, prepared.FinishedAt.Valid)
				}
				if n := eventCount(t, db, id, "step.resumed"); n != 1 {
					t.Errorf("resume receipts: %d", n)
				}
				stored, err := readStep(context.Background(), db, stepID)
				if err != nil {
					t.Error(err)
					return
				}
				if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: stored.Evidence, ID: stepID, Attempt: stored.Attempt, ExpectedStatus: stored.Status.String()})); err != nil {
					t.Error(err)
				}
			}}, stages: recoveryStageEffects{}, report: func(error) { reported = true }})
			d.restartPending(nil)
			d.wait()
			if called == failReceipt || reported != failReceipt {
				t.Fatalf("called=%v reported=%v", called, reported)
			}
			if failReceipt {
				state, err := newDispatchPlans(db, []string{"agent/pi"}).State(context.Background(), id, step.ID)
				if err != nil || !state.Orphaned {
					t.Fatalf("failed resume was not rolled back: %+v, %v", state, err)
				}
			}
		})
	}
}
