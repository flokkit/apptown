package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
)

// The assembled registry invokes the command owner and keeps its actual process
// evidence. Check a filesystem effect independently of Workflow's stored result.
func TestScriptRunnerBindingExecutesAndRecordsProcessEvidence(t *testing.T) {
	t.Parallel()
	a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "script", Uses: workflowexec.ScriptKind, With: map[string]any{"run": "printf executed > proof.txt; printf script-output; exit 7"}}}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{workflowexec.ScriptKind}
		deps.Runners = a.workDependencies().Runners
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snap.Steps) != 1 {
		t.Fatalf("admission: %+v %v", snap, err)
	}
	step := snap.Steps[0]
	a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID)
	snap, err = a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		ExitCode int    `json:"exitCode"`
		Log      string `json:"log"`
	}
	if err := json.Unmarshal(snap.Steps[0].Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if snap.Steps[0].Status != workflow.StatusFail || evidence.ExitCode != 7 || evidence.Log != "script-output" {
		t.Fatalf("process evidence: %+v, step=%+v", evidence, snap.Steps[0])
	}
	source := workflowSourceForTest(t, a, run)
	b, err := os.ReadFile(filepath.Join(source.Workspace(a.cfg.dataDir), "proof.txt"))
	if err != nil || string(b) != "executed" {
		t.Fatalf("process effect: %q %v", b, err)
	}
}
