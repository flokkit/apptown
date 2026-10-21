package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

func TestStepStateRejectsInvalidStoredRecords(t *testing.T) {
	t.Parallel()
	q, run, id := activityFixture(t)
	step, err := ReadStepIn(context.Background(), q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: "done", EvidenceJson: step.Evidence, ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String()})
	if err != nil || changed != 1 {
		t.Fatalf("store invalid step: changed=%d %v", changed, err)
	}
	if got, err := ReadStepIn(context.Background(), q.db, step.ID); got != nil || !errors.Is(err, errStepState) {
		t.Fatalf("invalid stored step escaped: %+v, %v", got, err)
	}
	if got, err := ReadStageStepsIn(context.Background(), q.db, run.ID, step.Stage); got != nil || !errors.Is(err, errStepState) {
		t.Fatalf("invalid stored list escaped: %+v, %v", got, err)
	}
}

func TestStepAttemptCannotFinishWithNonOutcome(t *testing.T) {
	t.Parallel()
	for _, state := range []StepState{StatusPending, StatusRunning, 0, 255} {
		t.Run(fmt.Sprint(uint8(state)), func(t *testing.T) {
			t.Parallel()
			q, _, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "scan"})
			step, err := ReadStepIn(context.Background(), q.db, id)
			if err != nil {
				t.Fatal(err)
			}
			attempts := newStepAttempts(q.db, func(kind string) bool { return kind == "scan" })
			step, err = attempts.Start(context.Background(), step, "scan")
			if err != nil {
				t.Fatal(err)
			}
			err = attempts.Finish(context.Background(), step, StepOutcome{Status: state, Evidence: []byte(`{"Result":"invalid"}`)})
			if !errors.Is(err, errStepState) && !errors.Is(err, errStepTransition) {
				t.Fatalf("invalid outcome admitted: %v", err)
			}
			if s := wantStepState(t, q.db, step.ID, StatusRunning); s.Attempt != 1 || !jsonEqual(s.Evidence, `{}`) || s.FinishedAt.Valid {
				t.Fatalf("claimed step: %+v", s)
			}
			wantEventCount(t, q.db, 0, ofKind("step.finished"))
			if err := attempts.Finish(context.Background(), step, StepOutcome{Status: StatusPass, Evidence: []byte(`{"Result":"valid"}`)}); err != nil {
				t.Fatal(err)
			}
			wantEventCount(t, q.db, 1, ofKind("step.finished"))
		})
	}
}
