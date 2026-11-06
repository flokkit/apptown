package dbschema

import "testing"

func TestRetirement215ReadsYAMLStepsRatherThanMatchingText(t *testing.T) {
	definitions := []workflow215{
		{name: "flow", yaml: `spec: {stages: [{steps: [{"uses": 'deploy'}]}]}`},
		{name: "alias", yaml: "step: &old {uses: deploy}\nspec: {stages: [{onEnter: [*old]}]}"},
		{name: "first", yaml: `spec: {stages: [{steps: [{uses: workflow/call, with: {workflow: flow}}]}]}`},
		{name: "second", yaml: `spec: {stages: [{steps: [{uses: workflow/call, with: {workflow: first}}]}]}`},
		{name: "script", yaml: "spec:\n  stages:\n    - steps:\n        - uses: script\n          with:\n            run: |\n              uses: deploy\n"},
	}
	for i := range definitions {
		definitions[i].origin = "stock"
	}
	got, err := removedWorkflows215(definitions)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"flow", "alias", "first", "second"} {
		if !got.names[name] {
			t.Errorf("did not retire %s", name)
		}
	}
	if got.names["script"] {
		t.Fatal("script contents treated as workflow steps")
	}
	local, err := removedWorkflows215([]workflow215{{name: "ship", yaml: `spec: {stages: [{steps: [{uses: build}]}]}`}})
	if err != nil || local.names["ship"] {
		t.Fatalf("supported local workflow retired: %v %v", local, err)
	}
	if _, err := removedWorkflows215([]workflow215{{name: "broken", yaml: "spec: ["}}); err == nil {
		t.Fatal("malformed stored YAML ignored")
	}
}
