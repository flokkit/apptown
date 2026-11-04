package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

type heldSummaryCounter struct{ changes []ids.ID }

func (c *heldSummaryCounter) RequestHeldSummaryIn(_ context.Context, _ *database.Tx, id ids.ID) error {
	c.changes = append(c.changes, id)
	return nil
}

// Evidence refresh asks only when the holding state changes, including approval.
func TestHeldCheckTransitionRequestsSummary(t *testing.T) {
	t.Parallel()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "report", Uses: "artifact/require", With: map[string]any{"kind": "report", "name": "final"}},
		{Name: "accept", Uses: "approval"},
	}}}})
	counter := &heldSummaryCounter{}
	s := newServiceWithAuthority(q.db, Dependencies{HeldSummaries: counter}, allowProject(run.ProjectID))
	t.Cleanup(s.Stop)
	for range 3 {
		if err := s.RefreshAssertions(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(counter.changes) != 1 || counter.changes[0] != run.ChangeID {
		t.Fatalf("held check requests: %v", counter.changes)
	}
	storedTextArtifact(t, q.db, run.ID, "report", "final", "commit", "")
	for range 3 {
		if err := s.RefreshAssertions(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(counter.changes) != 2 {
		t.Fatalf("approval transition requests: %v", counter.changes)
	}
}

func TestRepeatRefusalDoesNotRequestAnotherSummary(t *testing.T) {
	t.Parallel()
	for _, check := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "check"}[check], func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			if check {
				finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "tests", Type: "tests", ConfigJson: json.RawMessage(`{}`)}, StatusFail)
			}
			storedReadyStart(t, q.db, run.ID)
			counter := &heldSummaryCounter{}
			for range 3 {
				// Reconstruct advancement to exercise a repeated refusal after restart.
				a := newAdvancer(q.db, advanceRuntime{heldSummaries: counter, checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}})
				out, err := a.advanceRun(t.Context(), run, "system")
				if err != nil || out.Advanced || len(out.Blocking) == 0 {
					t.Fatalf("blocked advance: %+v %v", out, err)
				}
			}
			if len(counter.changes) != 1 || counter.changes[0] != run.ChangeID {
				t.Fatalf("requests: %v", counter.changes)
			}
		})
	}
}
func TestFailureAdviceReadsOnlyTheHeldCheckAndCurrentTargetActs(t *testing.T) {
	t.Parallel()
	q, run, approval := activityFixture(t)
	ctx := context.Background()
	check := finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "tests", Type: "tests"}, StatusFail)
	other := finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "review", Type: "agent/review"}, StatusFail)
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	in, err := s.FailureAdvice(ctx, check)
	if err != nil || in.Step.ID != check || in.Change == nil {
		t.Fatalf("inputs %+v: %v", in, err)
	}
	for _, a := range in.Acts {
		if (a.Act == nextOverride || a.Act == nextRetry) && a.StepID != check {
			t.Fatalf("offered another target: %+v", a)
		}
	}
	for _, id := range []ids.ID{other, approval} {
		if _, err := s.FailureAdvice(ctx, id); err == nil {
			t.Fatalf("advice for a step that does not hold the run: %s", id)
		}
	}
}

// A summary participant's durable write rolls back with the blocked-advance
// receipt, and is retried when the observation is recorded successfully.
func TestHeldSummaryRollsBackWithAdvanceReceipt(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	storedReadyStart(t, q.db, run.ID)
	requests := heldSummaryArtifact{run: run.ID}
	a := newAdvancer(q.db, advanceRuntime{heldSummaries: requests, checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}})
	dbtest.FailNextStatement(t, q.db, "INSERT INTO run_events")
	for _, want := range []int{0, 1} {
		if _, err := a.advanceRun(t.Context(), run, "system"); err != nil {
			t.Fatal(err)
		}
		artifacts, err := workflowsql.New(q.db.SQL()).ReadArtifacts(t.Context(), workflowsql.ReadArtifactsParams{RunID: run.ID})
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, artifact := range artifacts {
			if artifact.Kind == "summary-request" {
				got++
			}
		}
		if got != want {
			t.Fatalf("summary participant writes = %d, want %d", got, want)
		}
	}
}

type heldSummaryArtifact struct{ run ids.ID }

func (r heldSummaryArtifact) RequestHeldSummaryIn(ctx context.Context, tx *database.Tx, _ ids.ID) error {
	_, err := AddArtifactIn(ctx, tx, r.run, "summary-request", "held", "", "requested", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return err
}
