package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// Use the shipped definition and executor: moving a scanner across the merge
// boundary must fail this test even if stage-policy unit tests still pass.
func TestStockLandingImageFailureLeavesMainUntouchedAndApprovalUnavailable(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.Ways.SyncStockWorkflows(t.Context())
	pid, base, git := fleetRepo(t, a, "scan-before-merge", map[string]string{"a.txt": "seed"})
	if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "stock landing fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	attachScanner(t, a, "test-image", map[string]any{"image": "test:scanner", "command": []string{"scan"}, "report": ".forge/report.json", "format": "sarif", "target": "image", "kind": "vulnerability"})
	rebuildWork(t, a, func(d *workflow.Dependencies) {
		d.Runners = map[string]workflow.Runner{}
		for _, kind := range []string{"reconcile", "tests", "scan", "guidance/conformance", "deploy"} {
			d.Runners[kind] = func(e *workflow.Execution) {
				step, err := e.Claim()
				if err != nil {
					t.Error(err)
					return
				}
				status := workflow.StatusPass
				if strings.HasPrefix(step.Name, "scan-image") {
					status = workflow.StatusFail
				}
				if step.Type == "deploy" {
					t.Error("deployment ran after a failed image gate")
				}
				if err := e.Finish(workflow.StepOutcome{Status: status}, nil); err != nil {
					t.Error(err)
				}
			}
		}
	})
	ch := engineChange(t, a, a.projectByID(context.Background(), pid))
	// A real branch difference makes an accidental merge observable.
	ws := filepath.Join(a.cfg.dataDir, "workspaces", "change-"+fmt.Sprint(ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("change"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "a.txt")
	gittest.Run(t, ws, "commit", "-qm", "change")
	ctx := a.asForge(context.Background(), pid, "fixture")
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, step := range snap.Steps {
		if strings.HasPrefix(step.Name, "scan-image") && step.Status == workflow.StatusFail {
			failed = true
		}
		if step.Type == "approval" || step.Type == "deploy" {
			t.Fatalf("post-gate step already exists: %+v", step)
		}
	}
	if !failed {
		t.Fatalf("image scan did not block stock landing: %+v", snap.Steps)
	}
	if got := strings.TrimSpace(git("rev-parse", a.projectByID(ctx, pid).DefaultBranch)); got != base {
		t.Fatalf("main moved to %s from %s", got, base)
	}
	stored, err := a.Work.Change(ctx, ch.ID)
	if err != nil || stored.State != workflow.ChangeLanding {
		t.Fatalf("change after failed scan: %+v %v", stored, err)
	}
	for _, event := range snap.Events {
		if event.Kind == "merge.completed" {
			t.Fatal("merge receipt exists after failed scan")
		}
	}
	if snap.Run.Status != workflow.RunRunning || snap.Run.CurrentStage != "gates" {
		t.Fatalf("blocked landing: %+v", snap.Run)
	}
}
