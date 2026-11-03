package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRefreshAssertionsStopsBehindAnUnsettledAssertion(t *testing.T) {
	t.Parallel()
	q, run, approval := activityFixture(t)
	storedReadyStart(t, q.db, run.ID)
	transitionStoredStepForTest(t, q.db, approval, StatusPass)
	s := newServiceWithAuthority(q.db, Dependencies{}, allowProject(run.ProjectID))
	broken := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "broken", Type: "artifact/require", ConfigJson: []byte(`{"kind":[]}`), Status: StatusPending.String()})
	ready := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "ready", Type: "artifact/require", Status: StatusPending.String()})
	for _, row := range []struct{ stage, status, override string }{
		{"earlier", "pending", ""}, {"finish", "pass", ""}, {"finish", "fail", "reviewer"},
	} {
		id := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: row.stage, Name: "leave-alone", Type: "artifact/require", Status: row.status})
		if row.override != "" {
			// sql-guarantee: the selection law needs a failed overridden row; OverrideStep also changes status, reason, finish time, and emits the command receipt.
			completionExec(t, q.db, `UPDATE steps SET override_by=$1 WHERE id=$2`, row.override, id)
		}
	}
	insertTextArtifactForTest(t, q.db, run.ID, "report", "", "", "", time.Time{})
	if err := s.RefreshAssertions(context.Background(), run.ID); err == nil {
		t.Fatal("malformed assertion was not reported")
	}
	wantStepState(t, q.db, broken, StatusPending)
	wantStepState(t, q.db, ready, StatusPending)
	wantEventCount(t, q.db, 0, kindWith("check.evaluated", "step", "ready"))
	wantEventCount(t, q.db, 0, anyEvent)
	transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
	if err := s.RefreshAssertions(context.Background(), run.ID); err != nil {
		t.Fatalf("completed run should not reevaluate its checks: %v", err)
	}
	wantEventCount(t, q.db, 0, anyEvent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RefreshAssertions(ctx, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

// A review/scan verdict older than the latest agent work is reopened and
// re-dispatched; a verdict newer than the work stands.
func TestStaleVerdictReopens(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	storedReadyStart(t, q.db, run.ID)
	// sql-guarantee: stale-verdict recovery starts after the prior stage rows are removed; no owner command deletes every step of a live run.
	completionExec(t, q.db, `DELETE FROM steps WHERE run_id=$1`, run.ID)
	dispatched := map[ids.ID]bool{}
	runners := map[string]Runner{}
	for _, kind := range []string{"scan", "preview"} {
		runners[kind] = func(e *Execution) {
			dispatched[e.Step().ID] = true
			if _, err := e.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := e.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}
	}
	s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"scan", "preview"}, Runners: runners}, allowProject(run.ProjectID))

	// Agent finished at t2; a scan verdict finished at t1 (before the work)
	// and another at t3 (after it).
	// sql-guarantee: stale-verdict ordering depends on four fixed finished_at values; guarded step mutations stamp the current database time.
	_, _ = q.db.SQL().ExecContext(context.Background(), `INSERT INTO steps (id,run_id, stage, name, type, status, config_json, evidence_json, finished_at) VALUES
		($1,$2,'finish','work','agent/pi','pass','{}','{}','2026-01-01T00:00:02Z'),
		($3,$4,'finish','scan-old','scan','pass','{}','{}','2026-01-01T00:00:01Z'),
		($5,$6,'finish','scan-new','scan','pass','{}','{}','2026-01-01T00:00:03Z'),
		($7,$8,'finish','boot-skipped','preview','skipped','{}','{}','2026-01-01T00:00:01Z')`, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID)
	var oldID, newID, skippedID ids.ID
	for _, step := range storedSteps(t, q.db) {
		switch step.Name {
		case "scan-old":
			oldID = step.ID
		case "scan-new":
			newID = step.ID
		case "boot-skipped":
			skippedID = step.ID
		}
	}

	if err := s.RefreshAssertions(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	gotOld, gotNew, gotSkipped := dispatched[oldID], dispatched[newID], dispatched[skippedID]
	if !gotOld {
		t.Fatal("the stale verdict must be re-dispatched")
	}
	if !gotSkipped {
		t.Fatal("a skip that predates the work is stale too: the boot must run for the reworked draft (change 262)")
	}
	if gotNew {
		t.Fatal("a verdict newer than the work must stand")
	}
	if status := storedStep(t, q.db, oldID).Status; status != StatusPass {
		t.Fatalf("the reopened step ran, got %q", status)
	}
}

// An assertion waits for every earlier step even when its evidence has already
// arrived. Only a settled prerequisite lets the assertion pass or fail.
func TestAssertionWaitsForItsStageWorkBeforeFailing(t *testing.T) {
	t.Parallel()
	for _, sibling := range []StepState{StatusPending, StatusRunning, StatusPass, StatusFail, StatusNeedsHuman, StatusError, StatusSkipped} {
		t.Run(sibling.String(), func(t *testing.T) {
			t.Parallel()
			q, run, approval := activityFixture(t)
			storedReadyStart(t, q.db, run.ID)
			transitionStoredStepForTest(t, q.db, approval, StatusPass)
			s := newServiceWithAuthority(q.db, Dependencies{}, allowProject(run.ProjectID))
			insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "deploy", Type: "deploy", Status: sibling.String()})
			release := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "release-exists", Type: "artifact/require", ConfigJson: []byte(`{"kind":"release"}`), Status: StatusPending.String()})
			if err := s.RefreshAssertions(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			want := StatusFail
			if sibling == StatusPending || sibling == StatusRunning || sibling == StatusFail || sibling == StatusNeedsHuman || sibling == StatusError {
				want = StatusPending
			}
			if got := assertionStatusForTest(t, s, release); got != want {
				t.Fatalf("with deploy %s, release-exists is %s, want %s", sibling, got, want)
			}
			receipts := 0
			if want == StatusFail {
				receipts = 1
			}
			if got := evaluationReceiptsForTest(t, s, run.ID); got != receipts {
				t.Fatalf("with deploy %s, %d evaluation receipts, want %d", sibling, got, receipts)
			}
			insertTextArtifactForTest(t, q.db, run.ID, "release", "", "", "", time.Time{})
			if err := s.RefreshAssertions(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			want = StatusPass
			if sibling == StatusPending || sibling == StatusRunning || sibling == StatusFail || sibling == StatusNeedsHuman || sibling == StatusError {
				want = StatusPending
			}
			if got := assertionStatusForTest(t, s, release); got != want {
				t.Fatalf("attached release left release-exists %s", got)
			}
		})
	}
}

func assertionStatusForTest(t *testing.T, s *Service, id ids.ID) StepState {
	t.Helper()
	step, err := readStep(context.Background(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return step.Status
}

func evaluationReceiptsForTest(t *testing.T, s *Service, runID ids.ID) int {
	t.Helper()
	events, err := readWorkflowEvents(context.Background(), s.db, runID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "check.evaluated" {
			count++
		}
	}
	return count
}

// A later held assertion cannot prevent an earlier one from observing new
// evidence. Once the earlier one passes, the later one is rechecked too.
func TestTwoHeldAssertionsReevaluateInStageOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, run, first := assertionOwnerFixture(t, "scm/merged", `{}`)
	second := insertStoredStep(t, owner.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "second", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	merged := true
	owner.merge = mergeObserverFunc(func(context.Context, mergeAssertion) (MergeObservation, error) {
		return MergeObservation{Merged: merged}, nil
	})
	if got, err := owner.Evaluate(ctx, Assertion{StepID: first}); err != nil || got.Status != StatusPass {
		t.Fatalf("first observation: %+v %v", got, err)
	}
	merged = false
	for _, id := range []ids.ID{second, first} {
		if got, err := owner.Evaluate(ctx, Assertion{StepID: id}); err != nil || got.Status != StatusNeedsHuman {
			t.Fatalf("held assertion: %+v %v", got, err)
		}
	}
	merged = true
	svc := newServiceWithAuthority(owner.db, Dependencies{}, allowProject(run.ProjectID))
	svc.assertions.merge = owner.merge
	if err := svc.refreshAssertions(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []ids.ID{first, second} {
		wantStepState(t, owner.db, id, StatusPass)
	}
}
