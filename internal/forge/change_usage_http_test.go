package forge

import (
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A change's cost is what all its runs spent, not the run it is on now: a
// change that moved to a fresh run (a landing, a restart) kept its cost,
// where it had read $0 once it landed. Another change's spending is not
// counted.
func TestAChangesCostIsEveryRunsSpending(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	a, ctx, ch := changeScenario(t, spec)
	first, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	seedLibraryWorkflow(t, a, ch.ProjectID, "test", `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: test}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: land
      steps:
        - name: accept
          uses: approval
          with: {means: "Approve the landing"}
`)
	h := a.Handler()
	w := authzCall(h, "alice", "POST", fmt.Sprintf("/api/v1/runs/%d/restart-landing", first.ID), "")
	var restarted struct{ Run workflow.Run }
	if err := json.Unmarshal(w.Body.Bytes(), &restarted); err != nil || w.Code != 200 || restarted.Run.ID == first.ID {
		t.Fatalf("fresh run: %d %s", w.Code, w.Body.String())
	}
	ledger := usage.New(a.DB)
	for _, spent := range []struct {
		root        ids.ID
		total, cost int64
	}{{first.ID, 1000, 30000}, {restarted.Run.ID, 10, 500}, {idstest.N(999999), 7, 7}} {
		if err := ledger.Record(ctx, usage.Entry{Kind: "step", ProjectID: ch.ProjectID, RunID: spent.root, RootRunID: spent.root, Actor: "alice"}, usage.Totals{Total: spent.total, CostMicros: spent.cost}, ""); err != nil {
			t.Fatal(err)
		}
	}
	w = authzCall(h, "alice", "GET", fmt.Sprintf("/api/v1/changes/%d", ch.ID), "")
	var got struct {
		Usage struct{ Total, CostMicros int64 }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("detail: %d %s", w.Code, w.Body.String())
	}
	if got.Usage.Total != 1010 || got.Usage.CostMicros != 30500 {
		t.Fatalf("the change's cost is not its runs' spending: %+v", got.Usage)
	}
	if total, cost, err := ledger.RunTotal(ctx, restarted.Run.ID); err != nil || total != 10 || cost != 500 {
		t.Fatalf("one run's own figure: %d %d %v", total, cost, err)
	}
}
