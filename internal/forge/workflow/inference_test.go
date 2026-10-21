package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A runtime credential names only a step attempt. The observation derives the
// run and project from that step, and reports liveness and billing owner from
// the same snapshot, so a caller cannot pair one run's owner with another's step.
func TestStepAttemptObservationChecksScopeAttemptAndOwnerTogether(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"live", "missing step id", "negative attempt", "missing step", "foreign step", "stale attempt", "finished step", "done run", "abandoned run", "historical owner", "blank owner", "invalid state", "cancelled", "query failure"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			q, run, step := activityFixture(t)
			{
				current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
					t.Fatalf("stored step status: rows=%d err=%v", rows, err)
				}
			}
			if test != "historical owner" {
				actor := "payer"
				if test == "blank owner" {
					actor = ""
				}
				if _, err := workflowsql.New(q.db.SQL()).InsertRunInferenceOwner(context.Background(), workflowsql.InsertRunInferenceOwnerParams{RunID: run.ID, Actor: actor}); err != nil {
					t.Fatal(err)
				}
			}
			stepID := step
			attempt := storedStep(t, q.db, step).Attempt
			wantRun := RunRef{ID: run.ID, ProjectID: run.ProjectID}
			want := InferenceObservation{State: RunRunning, LiveAttempt: true, Owner: "payer"}
			var wantErr error
			invalid := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch test {
			case "missing step id":
				stepID = ids.Nil
				invalid = true
			case "negative attempt":
				attempt = -1
				invalid = true
			case "missing step":
				stepID = idstest.N(999999)
				wantErr = sql.ErrNoRows
			case "foreign step":
				// A step of another run reports that run and its owner, never
				// the owner recorded for the fixture's run.
				child, _ := activityChild(t, q.db, run, "other")
				stepID = insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: child.ID, Stage: "finish", Name: "foreign", Type: "script", Status: StatusRunning.String()})
				foreign, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), stepID)
				if err != nil {
					t.Fatal(err)
				}
				attempt = foreign.Attempt
				wantRun = RunRef{ID: child.ID, ProjectID: child.ProjectID}
				want.Owner = fmt.Sprintf("run/%d", child.ID)
			case "stale attempt":
				attempt++
				want.LiveAttempt = false
			case "finished step":
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
				want.LiveAttempt = false
			case "done run":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
				want.State = RunDone
			case "abandoned run":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "abandoned"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
				want.State = RunAbandoned
			case "historical owner":
				// Historical runs were inserted without an inference owner.
				want.Owner = fmt.Sprintf("run/%d", run.ID)
			case "blank owner":
				// The malformed blank owner was supplied at insertion.
				want.Owner = ""
			case "invalid state":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "unknown"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
				wantErr = errRunState
			case "cancelled":
				cancel()
				wantErr = context.Canceled
			case "query failure":
				dbtest.FailNextStatement(t, q.db, "SELECT r.id AS run_id")
				invalid = true
			}
			q.db.Pool().SetMaxOpenConns(1)
			ref, out, err := ObserveStepAttemptIn(ctx, q.db, stepID, attempt)
			if wantErr != nil || invalid {
				if err == nil || (wantErr != nil && !errors.Is(err, wantErr)) || out != (InferenceObservation{}) || ref != (RunRef{}) {
					t.Fatalf("invalid observation %+v %+v: %v", ref, out, err)
				}
			} else if err != nil || out != want || ref != wantRun {
				t.Fatalf("observation %+v %+v want %+v %+v: %v", ref, out, wantRun, want, err)
			}
		})
	}
}
