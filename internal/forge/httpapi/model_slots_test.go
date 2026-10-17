package httpapi

import (
	"testing"

	"flokkit.io/forge/internal/forge/wow"
)

func TestSlotConsumersNamesWorkflows(t *testing.T) {
	t.Parallel()
	ways := wow.New(databaseSuite.New(t), wow.Dependencies{})
	// A library workflow whose YAML names the 'plan' slot on a step.
	_, _, err := ways.PublishWorkflow(t.Context(), `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: planner}
spec:
  kind: lifecycle
  stages:
    - name: think
      steps:
        - {name: draft, uses: agent/pi, with: {model: plan}}
`, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	c, err := (ModelSlots{ways: ways}).slotConsumers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Built-in consumers are always present.
	if len(c["default"]) == 0 || len(c["chat"]) == 0 {
		t.Fatalf("built-in consumers missing: %+v", c)
	}
	// The 'plan' slot now has a workflow consumer naming the sample.
	found := false
	for _, s := range c["plan"] {
		if s == "workflow library/planner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan slot must list the workflow that names it: %+v", c["plan"])
	}
}
