package apptown

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestLandingRestartHTTPValidatesCurrentLaneBeforeAbandoningAndIdentifiesFreshRun(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	a, ctx, ch := changeScenario(t, spec)
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	path := fmt.Sprintf("/api/v1/runs/%d/restart-landing", run.ID)
	if w := authzCall(h, "alice", "POST", path, ""); w.Code != 422 {
		t.Fatalf("unavailable lane: %d %s", w.Code, w.Body.String())
	}
	prior, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || prior.Run.Status != workflow.RunRunning {
		t.Fatalf("preflight abandoned parked run: %+v %v", prior, err)
	}
	seedLibraryWorkflow(t, a, ch.ProjectID, "test", `apiVersion: apptown.dev/v1
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
	w := authzCall(h, "alice", "POST", path, "")
	var out struct{ Run workflow.Run }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Run.ID.IsZero() || out.Run.ID == run.ID || out.Run.ChangeID != ch.ID || out.Run.WorkflowName != "test" || out.Run.Status != workflow.RunRunning {
		t.Fatalf("fresh landing: %d %s %v", w.Code, w.Body.String(), err)
	}
	prior, err = a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || prior.Run.Status != workflow.RunAbandoned {
		t.Fatalf("old run not abandoned: %+v %v", prior, err)
	}
	runs, err := a.Work.ChangeRuns(ctx, ch.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("restart runs: %+v %v", runs, err)
	}
	if w := authzCall(h, "alice", "POST", path, ""); w.Code != 422 {
		t.Fatalf("duplicate old restart: %d %s", w.Code, w.Body.String())
	}
	runs, err = a.Work.ChangeRuns(ctx, ch.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("duplicate created work: %+v %v", runs, err)
	}
}
