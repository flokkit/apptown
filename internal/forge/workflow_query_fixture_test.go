package forge

import (
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// tv_ov_seed builds a project with a shipped change (two runs), an open
// runless change, and a running root run — the exact spread the overview
// partitions into active / open / history.
func tv_ov_seed(t *testing.T) (*App, ids.ID) {
	t.Helper()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	pid := createReadTestProject(t, a, "overview")
	begin := func(title string) ids.ID {
		id := createReadTestChange(t, a, pid, title)
		if _, err := a.Work.Begin(asPerson("bls"), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	approval := schema.Stage{Name: "work", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []any{"bls"}, "means": "the work is ready"}}}}
	shipped := begin("shipped one")
	finish := func(done *workflow.RunSnapshot) {
		t.Helper()
		if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: done.Steps[0].ID, Decision: workflow.VoteApprove}); err != nil {
			t.Fatal(err)
		}
		a.Work.Wait()
	}
	finish(startReadTestRun(t, a, shipped, approval))
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{approval}}})
	land, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: shipped, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	landSnapshot, err := a.Work.Snapshot(asPerson("bls"), land.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	finish(landSnapshot)
	begin("still cooking")
	active := begin("in flight")
	startReadTestRun(t, a, active, approval)
	return a, pid
}
