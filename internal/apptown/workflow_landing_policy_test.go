package apptown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// Law at the act: a landing parked across a policy amendment pauses at
// the merge with the next action stated; override proceeds; a relaxed
// law clears the pause on its own.
func TestLawAtTheAct(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	pid := createReadTestProject(t, a, "p")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	// A landing lane WITH a deploy step and no scan — legal when the run
	// started, illegal after the amendment below.
	landSpec := schema.Spec{Kind: "lifecycle", Role: "landing", Stages: []schema.Stage{
		{Name: "ship", Steps: []schema.Step{
			{Name: "ok", Uses: "approval", With: map[string]any{"approvers": []any{"bls"}, "means": "go"}},
			{Name: "deploy", Uses: "deploy"},
		}},
	}}
	chID := createReadTestChange(t, a, pid, "t")
	ch, _ := a.changeByID(context.Background(), chID)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	_ = os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644)
	gittest.Run(t, wt, "add", "f.txt")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")

	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"deploy"}
		deps.Report = func(err error) { t.Error(err) }
		deps.Runners = map[string]workflow.Runner{"deploy": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: landSpec})
	started, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: chID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("bls"), started.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("landing steps: %+v %v", snapshot, err)
	}
	runID, okStep := started.ID, snapshot.Steps[0].ID

	// The amendment lands WHILE the run waits at its approval.
	if err := a.Adoption.InstallPolicy(t.Context(), pid, "deploy-requires-scan", "Cannot deploy without a security check first", "requireStepBefore",
		map[string]any{"uses": "deploy", "requires": "scan"}, "stock", "bls"); err != nil {
		t.Fatal(err)
	}

	// The parked approval is granted. The merge must NOT happen; the
	// landing pauses with the next action in plain words.
	if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: okStep, Decision: workflow.VoteApprove, Comment: "", Adds: shownAdds(t, a, asPerson("bls"), okStep)}); err != nil {
		t.Fatal(err)
	}
	run, _ := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if run.Status != workflow.RunRunning {
		t.Fatalf("run should still be running (paused), is %q", run.Status)
	}
	snapshot, err = a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var law *workflow.Step
	for _, step := range snapshot.Steps {
		if step.Type == "policy/current" {
			law = step
		}
	}
	if law == nil {
		t.Fatal("no law step was raised")
	}
	if !strings.Contains(string(law.Evidence), "Restart landing") || !strings.Contains(string(law.Evidence), "override this check") {
		t.Fatalf("the next action must be stated plainly: %s", law.Evidence)
	}
	if branchMergedForTest(t, a, run) {
		t.Fatal("the merge happened under old law")
	}

	// Override with a reason: the recorded exit. The landing proceeds.
	if _, err := a.Work.Override(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "bls", Via: "session"}), workflow.Override{StepID: law.ID, Reason: "shipping the hotfix under the old rules; scan added next week"}); err != nil {
		t.Fatal(err)
	}
	run, _ = workflow.ReadRunIn(context.Background(), a.DB, runID)
	if !branchMergedForTest(t, a, run) {
		t.Fatal("override should let the landing proceed")
	}
	if run.Status != workflow.RunDone {
		t.Fatalf("landing should complete after override: %q", run.Status)
	}
}

// The relaxed-law path: if the policy is repealed while paused, the
// pause clears itself on the next advance.
func TestLawRelaxesClearsPause(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "p")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	landSpec := schema.Spec{Kind: "lifecycle", Role: "landing", Stages: []schema.Stage{
		{Name: "ship", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}}}
	chID := createReadTestChange(t, a, pid, "t")
	ch, _ := a.changeByID(context.Background(), chID)
	_ = prepareChangeWorkspaceForTest(a, p, ch)
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	_ = os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644)
	gittest.Run(t, wt, "add", "f.txt")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")
	if err := a.Adoption.InstallPolicy(t.Context(), pid, "x", "", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"}, "stock", "test"); err != nil {
		t.Fatal(err)
	}
	policies, err := a.Adoption.ProjectPolicies(context.Background(), a.DB, pid)
	if err != nil || len(policies) != 1 || policies[0].Name != "x" {
		t.Fatalf("installed policy: %+v %v", policies, err)
	}
	policyID := policies[0].ID
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"deploy"}
		deps.Report = func(err error) { t.Error(err) }
		deps.Runners = map[string]workflow.Runner{"deploy": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: landSpec})
	started, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: chID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	runID := started.ID
	snapshot, err := a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var lawID ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "policy/current" && step.Status == workflow.StatusNeedsHuman {
			lawID = step.ID
		}
	}
	if lawID.IsZero() {
		t.Fatalf("law step did not need a person: %+v", snapshot.Steps)
	}
	if err := a.Adoption.RemovePolicy(t.Context(), policyID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := advanceAs(a, runID, "test"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	lawCleared := false
	for _, step := range snapshot.Steps {
		if step.ID == lawID {
			lawCleared = step.Status == workflow.StatusPass
		}
	}
	if !lawCleared {
		t.Fatalf("repealed law must clear the pause: %+v", snapshot.Steps)
	}
	run, _ := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if !branchMergedForTest(t, a, run) {
		t.Fatal("landing should proceed once the law relaxes")
	}
}
