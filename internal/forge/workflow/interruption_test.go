package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
)

func TestInterruptedStepsPreservesOnlyOwnedLiveChildCalls(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []string{"none", "project", "attempt", "parent", "identity", "finished"} {
		t.Run(mismatch, func(t *testing.T) {
			t.Parallel()
			db, run, _ := completionFixture(t, "working")
			q := queries{db: db}
			insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "script", Status: StatusRunning.String()})
			call := insertStoredCall(t, q.db, run.ID, "finish", "child", 2, nil)
			childProject, parentID := run.ProjectID, run.ID
			if mismatch == "project" {
				childProject = workflowTestProject(t, q.db, "other", "Other", "")
			}
			if mismatch == "parent" {
				parentID = ids.Nil
			}
			child := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: childProject, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{}`), ParentRunID: parentID, ParentStepID: call})
			if err := requireRunWrite(workflowsql.New(q.db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ID: child.ID, ParentStepAttempt: 2})); err != nil {
				t.Fatal(err)
			}
			recordStoredCallEvidence(t, q.db, call, 2, []byte(fmt.Sprintf(`{"childRunId":%q,"workflow":"child"}`, child.ID)))
			switch mismatch {
			case "project":
				// The child was inserted with the foreign project.
			case "attempt":
				if rows, err := workflowsql.New(q.db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ID: child.ID, ParentStepAttempt: 1}); err != nil || rows != 1 {
					t.Fatalf("stored parent attempt: rows=%d err=%v", rows, err)
				}
			case "parent":
				// The child was inserted without a parent relationship.
			case "identity":
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), call)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: call, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: current.Status, EvidenceJson: []byte(`{}`)}); err != nil || rows != 1 {
						t.Fatalf("stored step state: rows=%d err=%v", rows, err)
					}
				}
			case "finished":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: child.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: child.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
			}
			for pass := 0; pass < 2; pass++ {
				out, err := database.Transact(context.Background(), q.db, func(tx *database.Tx) (InterruptedSteps, error) {
					return InterruptStepsIn(context.Background(), tx, nil, nil, time.Now())
				})
				if err != nil {
					t.Fatal(err)
				}
				want := InterruptedSteps{Interrupted: 2}
				if mismatch == "none" {
					want = InterruptedSteps{Interrupted: 1, PreservedCalls: 1}
				}
				if pass == 1 {
					want.Interrupted = 0
				}
				if out != want {
					t.Fatalf("report %+v want %+v", out, want)
				}
			}
			want := StatusError
			if mismatch == "none" {
				want = StatusRunning
			}
			wantStepState(t, q.db, call, want)
		})
	}
}

func TestInterruptedStepsRefusesSuppressedWritesAndRollsBack(t *testing.T) {
	t.Parallel()
	q, _, step := activityFixture(t)
	{
		current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	completionExec(t, q.db, `CREATE FUNCTION veto_interrupt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, q.db, `CREATE TRIGGER veto_interrupt BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_interrupt()`)
	out, err := database.Transact(context.Background(), q.db, func(tx *database.Tx) (InterruptedSteps, error) {
		return InterruptStepsIn(context.Background(), tx, nil, nil, time.Now())
	})
	if err == nil || out != (InterruptedSteps{}) {
		t.Fatalf("suppressed repair: %+v %v", out, err)
	}
	wantEqual(t, "finished", wantStepState(t, q.db, step, StatusRunning).FinishedAt.Valid, false)
	completionExec(t, q.db, `DROP TRIGGER veto_interrupt ON steps`)
	failure := fmt.Errorf("later startup participant failed")
	err = q.db.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := InterruptStepsIn(context.Background(), tx, nil, nil, time.Now()); err != nil {
			return err
		}
		return failure
	})
	if err != failure {
		t.Fatal(err)
	}
	wantEqual(t, "finished", wantStepState(t, q.db, step, StatusRunning).FinishedAt.Valid, false)
}

// A restart ends a running step of a type it does not continue: it is in
// error with the reason that says so, and is not the restart's orphan, which
// the dispatcher resumes. Any other running step is that orphan.
func TestARestartEndsAStepItDoesNotContinue(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	deploy := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "ship", Type: "deploy", Status: StatusRunning.String()})
	script := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "make", Type: "script", Status: StatusRunning.String()})
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (InterruptedSteps, error) {
		return InterruptStepsIn(context.Background(), tx, nil, []string{"deploy"}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	plans := newDispatchPlans(db, []string{"deploy", "script"})
	for id, orphan := range map[ids.ID]bool{deploy: false, script: true} {
		state, err := plans.State(context.Background(), run.ID, id)
		if err != nil || state.Status != StatusError || state.Orphaned != orphan {
			t.Errorf("step %d after the restart: %+v %v, want orphaned %v", id, state, err, orphan)
		}
	}
	ended, err := ReadStepIn(context.Background(), db, deploy)
	if err != nil || !strings.Contains(string(ended.Evidence), EndedByRestart) {
		t.Errorf("the ended step's reason: %s %v", ended.Evidence, err)
	}
}

// A check's process is killed with a stopping forge (systemd signals the
// whole service), so its runner sees a scanner exit 143 that says nothing
// about the work. Once the forge is shutting down, that error is not
// recorded: the step stays running, startup repair makes it the restart's
// orphan, and the restarted forge runs it again. The same exit while the
// forge keeps running (the scanner's own OOM kill) is recorded as an error,
// and a pass during shutdown is recorded: only a pass is evidence then.
//
// Kills: Settle recording every outcome during shutdown; Settle refusing
// outside shutdown; refusing a pass during shutdown.
func TestAStepThatFailsWhileTheForgeStopsIsRunAgainAfterTheRestart(t *testing.T) {
	t.Parallel()
	killed := map[string]any{"reason": "scanner error (exit 143)", "exitCode": 143}
	for _, tc := range []struct {
		name     string
		shutdown bool
		outcome  StepState
		want     StepState
	}{
		{"error while AppTown keeps running", false, StatusError, StatusError},
		{"error while AppTown stops", true, StatusError, StatusRunning},
		{"failure while AppTown stops", true, StatusFail, StatusRunning},
		{"pass while AppTown stops", true, StatusPass, StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, run, _ := completionFixture(t, "working")
			scan := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "scan-source:semgrep", Type: "scan", Status: StatusPending.String()})
			entered, release := make(chan struct{}), make(chan struct{})
			settled := make(chan error, 1)
			service := newServiceWithAuthority(db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": func(task *Execution) {
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				close(entered)
				<-release
				settled <- task.Settle(EncodeOutcome(tc.outcome, killed, nil))
			}}, Report: func(err error) { t.Log(err) }}, allowProject(run.ProjectID))
			service.dispatcher.prepare = nil
			service.dispatcher.advance = func(ids.ID) {}
			t.Cleanup(service.Stop)
			service.Dispatch(DispatchPlan{RunID: run.ID, Steps: []ReadyStep{{ID: scan, Type: "scan", Name: "scan-source:semgrep"}}})
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the step's runner did not start")
			}
			var stopped <-chan struct{}
			if tc.shutdown {
				stopped = service.BeginShutdown()
			}
			close(release)
			err := <-settled
			if tc.want == StatusRunning {
				if !errors.Is(err, ErrShuttingDown) {
					t.Fatalf("settling during shutdown: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if stopped != nil {
				<-stopped
			}
			service.Wait()
			wantStepState(t, db, scan, tc.want)
			if tc.want != StatusRunning {
				return
			}
			if _, err := database.Transact(ctx, db, func(tx *database.Tx) (InterruptedSteps, error) {
				return InterruptStepsIn(ctx, tx, nil, nil, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			restarted := newServiceWithAuthority(db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": func(task *Execution) {
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				runs++
				if err := task.Settle(EncodeOutcome(StatusPass, map[string]any{"exitCode": 0}, nil)); err != nil {
					t.Error(err)
				}
			}}, Report: func(err error) { t.Error(err) }}, allowProject(run.ProjectID))
			restarted.dispatcher.prepare = nil
			restarted.dispatcher.advance = func(ids.ID) {}
			t.Cleanup(restarted.Stop)
			restarted.ResumePending()
			restarted.Wait()
			if runs != 1 {
				t.Fatalf("the restarted forge ran the step %d times, want once", runs)
			}
			wantStepState(t, db, scan, StatusPass)
		})
	}
}
