package forge

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A run's whole life from YAML: birth materializes the steps a workflow
// promises (config included), approvals advance it stage by stage, the
// in-flight guard holds, and the run ends done. This is the path every
// run is born through — it had no test.
func TestRunLifecycleFromYAML(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	p := engineProject(t, a, "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	seedLibraryWorkflow(t, a, idstest.N(1), "flow", lifecycleYAML)
	ch := engineChange(t, a, p)

	run, err := a.Work.StartRun(a.asForge(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "flow"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.RunRunning || run.CurrentStage != "decide" {
		t.Fatalf("birth: %+v", run)
	}
	// The steps a workflow promises exist, config intact.
	snapshot, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var stepID ids.ID
	for _, step := range snapshot.Steps {
		if step.Name == "first-ok" {
			stepID = step.ID
			if !strings.Contains(string(step.Config), "the direction is right") {
				t.Fatalf("step config lost the standard of approval: %s", step.Config)
			}
		}
	}
	if stepID.IsZero() {
		t.Fatal("stage-1 step not materialized")
	}

	// One run per change: a second start is refused while this one lives.
	if _, err := a.Work.StartRun(a.asForge(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "flow"}); err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("in-flight guard: %v", err)
	}

	// Approval advances the stage; the second stage's step materializes.
	if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: stepID, Decision: workflow.VoteApprove, Comment: ""}); err != nil {
		t.Fatal(err)
	}
	run, _ = workflow.ReadRunIn(context.Background(), a.DB, run.ID)
	if run.CurrentStage != "confirm" {
		t.Fatalf("first approval should advance to confirm, at %q", run.CurrentStage)
	}
	snapshot, err = a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var step2 ids.ID
	for _, step := range snapshot.Steps {
		if step.Name == "second-ok" {
			step2 = step.ID
		}
	}
	if step2.IsZero() {
		t.Fatal("stage-2 step not materialized")
	}
	if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: step2, Decision: workflow.VoteApprove, Comment: "", Adds: shownAdds(t, a, asPerson("bls"), step2)}); err != nil {
		t.Fatal(err)
	}
	run, _ = workflow.ReadRunIn(context.Background(), a.DB, run.ID)
	if run.Status != workflow.RunDone {
		t.Fatalf("final approval should finish the run, status %q", run.Status)
	}
}
