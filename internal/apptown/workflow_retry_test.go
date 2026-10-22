package apptown

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/riverqueue/river/rivertype"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Transport keeps the domain refusal's text even when an operation wraps it.

// Work owns retry state; AppTown must still prove that queued cleanup and the
// continuation roll back together when a later required write fails.
func TestSendBackRollsBackQueuedAbandonment(t *testing.T) {
	t.Parallel()
	a, ctx, source := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "build", Uses: "agent/pi"}}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Fatal(err)
			}
		}}
	})
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, source.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("source: %+v %v", snapshot, err)
	}
	if err := a.Work.ExecuteStep(ctx, snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if out, err := a.Work.Advance(ctx, source.ID); err != nil || out.RunStatus != workflow.RunDone {
		t.Fatalf("complete source: %+v %v", out, err)
	}
	landing, err := a.Work.RequestLanding(ctx, source.ChangeID)
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	beforeJobs := queueRecord(t, a)
	beforeWork := workflowResetSnapshot(t, a.DB)
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO workflow_continuations")
	if _, err := a.Work.SendBack(ctx, workflow.SendBack{ChangeID: source.ChangeID, Note: "revise the work"}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("continuation failure: %v", err)
	}
	if afterJobs := queueRecord(t, a); afterJobs != beforeJobs || workflowResetSnapshot(t, a.DB) != beforeWork {
		t.Fatal("failed send-back left work or queued cleanup")
	}
	retried, err := a.Work.SendBack(ctx, workflow.SendBack{ChangeID: source.ChangeID, Note: "revise the work"})
	if err != nil || retried.RunID == source.ID {
		t.Fatalf("explicit send-back: %+v %v", retried, err)
	}
	ended, err := a.Work.Snapshot(ctx, landing.ID, workflow.DetailView)
	if err != nil || ended.Run.Status != workflow.RunAbandoned {
		t.Fatalf("superseded landing: %+v %v", ended, err)
	}
	requireJobs(t, a, 1, func(_ *rivertype.JobRow, r jobs.Request) bool { return r.Operation == workflowAbandonmentJob })
}
