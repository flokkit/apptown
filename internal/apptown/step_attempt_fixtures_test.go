package apptown

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func attemptFixture(t *testing.T, kind string, configs ...Config) (*App, *workflow.Run, *workflow.Step) {
	t.Helper()
	a, ctx, change := changeScenarioIn(t, testApp(t, configs...), schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "check", Uses: kind}}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{kind}
		deps.Runners = a.workDependencies().Runners
		deps.Report = a.workDependencies().Report
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Steps) != 1 {
		t.Fatalf("runner steps: %d", len(snapshot.Steps))
	}
	return a, snapshot.Run, snapshot.Steps[0]
}
