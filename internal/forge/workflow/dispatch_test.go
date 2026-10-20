package workflow

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func dispatchFixture(t *testing.T) (dispatchPlans, *Run, ids.ID) {
	t.Helper()
	db, run, _ := completionFixture(t, "working")
	q := queries{db: db}
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "script", Status: StatusPending.String()})
	return newDispatchPlans(q.db, []string{"script", "scan", "agent/pi"}), run, id
}
func TestDispatchPlanKeepsHistoricalRowsAndRegistry(t *testing.T) {
	t.Parallel()
	db, run, _ := completionSpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "image", Uses: "scan", With: map[string]any{"on": "image"}}}, Steps: []schema.Step{{Name: "image:explicit", Uses: "scan"}}}}})
	kinds := []string{"script", "scan"}
	owner := newDispatchPlans(db, kinds)
	kinds[0] = "approval"
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "image:historical", Type: "scan", Status: StatusPass.String()})
	gate := insertStoredStep(t, owner.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "image:explicit", Type: "scan", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	script := insertStoredStep(t, owner.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "script", Type: "script", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	insertStoredStep(t, owner.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "old", Name: "old", Type: "script", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	plan, err := owner.Plan(context.Background(), run.ID)
	want := []ReadyStep{{id, "scan", true, "image:historical"}, {gate, "scan", false, "image:explicit"}, {script, "script", false, "script"}}
	if err != nil || !reflect.DeepEqual(plan.Steps, want) || plan.Pending != 2 {
		t.Fatalf("historical plan: %+v %v", plan, err)
	}
	plan.Steps[0].Name = "changed"
	again, err := owner.Plan(context.Background(), run.ID)
	if err != nil || !reflect.DeepEqual(again.Steps, want) {
		t.Fatalf("aliased plan: %+v %v", again, err)
	}
	ids, err := owner.RestartCandidates(context.Background())
	if err != nil || !slices.Contains(ids, run.ID) {
		t.Fatalf("bound registry: %v %v", ids, err)
	}
	// A quote in a runner kind is data, never SQL. An empty registry selects no steps.
	for _, types := range [][]string{{"script' OR true --"}, nil} {
		empty, err := newDispatchPlans(owner.db, types).RestartCandidates(context.Background())
		if err != nil || len(empty) != 0 {
			t.Fatalf("registry %q: %v %v", types, empty, err)
		}
	}
}
func TestDispatchPlanUsesOneSnapshot(t *testing.T) {
	t.Parallel()
	db, run, _ := completionSpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}, {Name: "later"}}})
	owner := newDispatchPlans(db, []string{"script"})
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "script", Status: StatusPending.String()})
	gate, err := owner.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := gate.Exec(`LOCK TABLE steps IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		plan DispatchPlan
		err  error
	}
	done := make(chan result, 1)
	go func() { p, e := owner.Plan(context.Background(), run.ID); done <- result{p, e} }()
	dbtest.WaitForLock(t, owner.db, "FROM steps WHERE run_id")
	{
		current, err := workflowsql.New(owner.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := requireRunWrite(workflowsql.New(owner.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: "later", NextStatus: current.Status})); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: No owner mutation moves an existing step's stage; rehome that same row inside the blocking transaction to test snapshot consistency.
	if _, err := gate.Exec(`UPDATE steps SET stage='later' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowsql.New(gate.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "later", Name: "later", Type: "script", ConfigJson: []byte(`{}`), Status: StatusPending.String()}); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.plan.Steps) != 1 || got.plan.Steps[0].ID != id {
			t.Fatalf("mixed snapshot: %+v %v", got.plan, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plan did not finish")
	}
}
func TestDispatchTransitionsRequireCurrentScopeAndExplicitRestart(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"skip", "resume"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			for _, state := range []string{"ordinary error", "finished", "wrong run", "old stage", "closed run", "manual step", "override"} {
				t.Run(state, func(t *testing.T) {
					t.Parallel()
					db, run, _ := completionFixture(t, "working")
					owner := newDispatchPlans(db, []string{"script", "scan", "agent/pi"})
					typ := "script"
					if state == "manual step" {
						typ = "approval"
					}
					id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: typ, Status: StatusPending.String()})
					status := StatusPending
					if operation == "resume" {
						status = StatusError
					}
					{
						current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						if rows, err := workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: status.String(), EvidenceJson: []byte(`{"reason":"orphaned by apptownd restart"}`)}); err != nil || rows != 1 {
							t.Fatalf("stored step state: rows=%d err=%v", rows, err)
						}
					}
					target := run.ID
					switch state {
					case "ordinary error":
						{
							current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
							if err != nil {
								t.Fatal(err)
							}
							if rows, err := workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusError.String(), EvidenceJson: []byte(`{"reason":"failed normally"}`)}); err != nil || rows != 1 {
								t.Fatalf("stored step state: rows=%d err=%v", rows, err)
							}
						}
					case "finished":
						{
							current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
							if err != nil {
								t.Fatal(err)
							}
							if rows, err := workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
								t.Fatalf("stored step status: rows=%d err=%v", rows, err)
							}
						}
					case "wrong run":
						target = idstest.N(999999)
					case "old stage":
						if err := requireRunWrite(workflowsql.New(owner.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision, NextStage: "later", NextStatus: run.Status.String()})); err != nil {
							t.Fatal(err)
						}
					case "manual step":
						// The manual kind was supplied at insertion.
					case "override":
						// sql-guarantee: Retain a pending/error row with an override actor to test dispatch refusal; OverrideStep would turn it into a passed row.
						completionExec(t, owner.db, `UPDATE steps SET override_by='person' WHERE id=?`, id)
					case "closed run":
						{
							current, err := workflowsql.New(owner.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
							if err != nil {
								t.Fatal(err)
							}
							if rows, err := workflowsql.New(owner.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "abandoned"}); err != nil || rows != 1 {
								t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
							}
						}
					}
					if operation == "skip" {
						if err := owner.Skip(context.Background(), target, id, "prior"); err != nil {
							t.Fatal(err)
						}
					} else {
						ok, err := owner.Resume(context.Background(), target, id)
						if err != nil || ok {
							t.Fatalf("resumed stale work: %v %v", ok, err)
						}
					}
					wantEventCount(t, owner.db, 0, ofKind("step.skipped", "step.resumed"))
				})
			}
		})
	}
}
func TestDispatchTransitionsRequireWritesAndReceipts(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"skip", "resume"} {
		for _, failure := range []string{"none", "write", "suppressed", "receipt"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				t.Parallel()
				db, run, _ := completionFixture(t, "working")
				owner := newDispatchPlans(db, []string{"script", "scan", "agent/pi"})
				before, after := StatusPending, StatusSkipped
				if operation == "resume" {
					before, after = StatusError, StatusPending
				}
				id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "agent/pi", Status: before.String(), ConfigJson: []byte(`{"n":9007199254740993,"extra":{"keep":true}}`)})
				{
					current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: before.String(), EvidenceJson: []byte(`{"reason":"orphaned by apptownd restart"}`)}); err != nil || rows != 1 {
						t.Fatalf("stored step state: rows=%d err=%v", rows, err)
					}
				}
				switch failure {
				case "write":
					dbtest.FailNextStatement(t, owner.db, "UPDATE steps SET status=")
				case "receipt":
					dbtest.FailNextStatement(t, owner.db, "INSERT INTO run_events")
				case "suppressed":
					completionExec(t, owner.db, `CREATE FUNCTION veto_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
					completionExec(t, owner.db, `CREATE TRIGGER veto_dispatch BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_dispatch()`)
				}
				var err error
				if operation == "skip" {
					err = owner.Skip(context.Background(), run.ID, id, "prior")
				} else {
					var ok bool
					ok, err = owner.Resume(context.Background(), run.ID, id)
					if ok != (failure == "none") {
						t.Errorf("resume permission=%v failure=%s err=%v", ok, failure, err)
					}
				}
				if (err == nil) != (failure == "none") {
					t.Fatalf("failure=%s err=%v", failure, err)
				}
				count := 1
				if failure != "none" {
					count = 0
					after = before
				}
				wantEventCount(t, owner.db, count, ofKind("step.skipped", "step.resumed"))
				wantJSONField(t, "config", storedStep(t, owner.db, id).Config, "n", "9007199254740993", "extra.keep", "true")
				wantStepState(t, owner.db, id, after)
				if failure == "none" && operation == "resume" {
					wantJSONField(t, "config", storedStep(t, owner.db, id).Config, "retryMode", "continue")
				}
			})
		}
	}
}
func TestDispatchTransitionCancelsItsLockWait(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"skip", "resume"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			owner, run, id := dispatchFixture(t)
			gate, err := owner.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback() }()
			if _, err := workflowsql.New(gate.SQL()).ProjectForUpdate(context.Background(), run.ProjectID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if operation == "skip" {
					done <- owner.Skip(ctx, run.ID, id, "prior")
				} else {
					_, err := owner.Resume(ctx, run.ID, id)
					done <- err
				}
			}()
			dbtest.WaitForLock(t, owner.db, "-- name: ProjectForUpdate")
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wait did not cancel")
			}
			wantEventCount(t, owner.db, 0, ofKind("step.skipped", "step.resumed"))
		})
	}
}
