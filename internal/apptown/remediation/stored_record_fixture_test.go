package remediation

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/database"
)

// These sparse records exercise the owner's projections without requiring
// review provenance unrelated to the field being projected.
func insertStoredRemediation(t *testing.T, db *database.DB, arg remediationsql.InsertRemediationParams) ids.ID {
	t.Helper()
	if arg.Mode == "" {
		arg.Mode = "attended"
	}
	if arg.CreatedAt.IsZero() {
		arg.CreatedAt = time.Now()
	}
	if arg.UpdatedAt.IsZero() {
		arg.UpdatedAt = arg.CreatedAt
	}
	if arg.Number == 0 {
		number, err := queries(db).NextRemediationNumber(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		arg.Number = number
	}
	row, err := queries(db).InsertRemediation(context.Background(), arg)
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func insertStoredItem(t *testing.T, db *database.DB, arg remediationsql.InsertItemParams, attempt int, runID ids.ID) ids.ID {
	t.Helper()
	if arg.UpdatedAt.IsZero() {
		arg.UpdatedAt = time.Now()
	}
	row, err := queries(db).InsertItem(context.Background(), arg)
	if err != nil {
		t.Fatal(err)
	}
	for range attempt {
		requireStoredWrite(t)(queries(db).StartItem(context.Background(), remediationsql.StartItemParams{UpdatedAt: time.Now(), ID: row.ID, RunID: runID}))
	}
	if attempt > 0 {
		requireStoredWrite(t)(queries(db).SetItemState(context.Background(), remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: row.ID, State: arg.State}))
	}
	return row.ID
}

func requireStoredWrite(t *testing.T) func(int64, error) {
	t.Helper()
	return func(n int64, err error) {
		t.Helper()
		if err != nil || n != 1 {
			t.Fatalf("stored mutation affected %d rows: %v", n, err)
		}
	}
}

// Admission fixes declaration and row ordering; explicit claims and completions
// supply the activity status through Work's supported runner protocol.
func admitPageRun(t *testing.T, db *database.DB, projectID ids.ID, name string, steps []schema.Step, outcomes map[string]workflow.StepState) ids.ID {
	t.Helper()
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}, Verified: true, Receipt: "system"})
	if _, err := identity.New(db, identity.Config{}).EnsureUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	var status workflow.StepState
	runner := func(e *workflow.Execution) {
		if _, err := e.Claim(); err != nil {
			t.Fatal(err)
		}
		if status != workflow.StatusRunning {
			if err := e.Finish(workflow.StepOutcome{Status: status, Evidence: []byte(`{}`)}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	work := workflow.New(context.Background(), db, workflow.Dependencies{Authority: reconcileActor{}, Automatic: []string{"script", "agent/pi", "agent/review", "preview"}, Runners: map[string]workflow.Runner{"script": runner, "agent/pi": runner, "agent/review": runner, "preview": runner}})
	work.Stop()
	def := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: name, Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "build", Steps: steps}}}}
	run, err := database.Transact(ctx, db, func(tx *database.Tx) (*workflow.Run, error) {
		return work.AdmitRunIn(ctx, tx, workflow.StartRequest{ProjectID: projectID, WorkflowName: name, InputsJSON: "{}"}, def)
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareRemediationRun(t, db, run.ID)
	snapshot, err := work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		next, ok := outcomes[step.Name]
		status = next
		if !ok || status == workflow.StatusPending {
			continue
		}

		if err := work.ExecuteStep(ctx, step.ID); err != nil {
			t.Fatal(err)
		}
	}
	return run.ID
}

func enterRemediationStage(t *testing.T, f *attemptScenario, runID ids.ID, stage string, steps ...schema.Step) map[string]ids.ID {
	t.Helper()
	spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: stage, Steps: steps}}}
	if err := f.db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.NewStages(nil, func(string) bool { return false }).EnterIn(f.ctx, tx, runID, &spec, stage, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.work.Snapshot(f.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	stepIDs := map[string]ids.ID{}
	for _, step := range snapshot.Steps {
		if step.Stage == stage {
			stepIDs[step.Name] = step.ID
		}
	}
	return stepIDs
}

func moveRemediationRun(t *testing.T, f *attemptScenario, runID ids.ID, state workflow.RunState, stage string) {
	t.Helper()
	if err := f.db.InTx(context.Background(), func(tx *database.Tx) error {
		current, err := workflow.ReadRunIn(f.ctx, tx, runID)
		if err != nil {
			return err
		}
		return workflow.TransitionRunIn(f.ctx, tx, current, state, stage, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
}
