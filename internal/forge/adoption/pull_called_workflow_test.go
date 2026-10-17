package adoption

import (
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/wow"
)

const pullWorkflowYAML = `
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: { name: flow }
spec:
  kind: lifecycle
  stages:
    - name: decide
      steps:
        - name: first-ok
          uses: approval
          with: { approvers: [bls], means: "the direction is right" }
    - name: confirm
      steps:
        - name: second-ok
          uses: approval
          with: { approvers: [bls], means: "the result is right" }
`

// A pack removal cannot deselect a workflow called by a surviving lane.
func TestPackPullKeepsCalledWorkflows(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "p", "P")
	ways := wow.New(db, wow.Dependencies{})
	adopt := New(db, Dependencies{Ways: ways})
	caller := strings.Replace(strings.Replace(pullWorkflowYAML,
		"metadata: { name: flow }", "metadata: { name: land }", 1),
		"uses: approval\n          with: { approvers: [bls], means: \"the direction is right\" }",
		"uses: workflow/call\n          with: { workflow: ship }", 1)
	shipY := strings.Replace(pullWorkflowYAML, "metadata: { name: flow }", "metadata: { name: ship }", 1)
	for _, x := range []struct{ name, yaml string }{{"land", caller}, {"ship", shipY}} {
		name, version, err := ways.PublishWorkflow(t.Context(), x.yaml, "fixture")
		if err != nil || name != x.name || version != 1 {
			t.Fatalf("published workflow = %s@%d, want %s@1: %v", name, version, x.name, err)
		}
		if err := adopt.SelectLane(t.Context(), pid, x.name, true, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := json.Marshal(map[string]any{"name": "pk", "workflows": []string{"land"}})
	if err != nil {
		t.Fatal(err)
	}
	if version, err := ways.PublishPack(t.Context(), "pk", "", string(spec), "stock"); err != nil || version != 1 {
		t.Fatalf("publish pack: version=%d err=%v", version, err)
	}
	pack, err := ways.Pack(t.Context(), "pk")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adopt.PackPullPlan(t.Context(), pid, *pack)
	if err != nil || len(plan.Removals) != 0 || len(plan.KeptReferenced) != 1 || plan.KeptReferenced[0] != "ship" {
		t.Fatalf("called workflow must be kept, loudly: removals=%v keptReferenced=%v err=%v", plan.Removals, plan.KeptReferenced, err)
	}
}
