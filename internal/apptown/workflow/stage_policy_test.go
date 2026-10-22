package workflow

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

func TestWorkflowGateMissingAndOptionalEvidence(t *testing.T) {
	t.Parallel()
	stage := schema.Stage{Name: "review", Steps: []schema.Step{{Name: "must", Uses: "approval"}, {Name: "advisory", Uses: "script"}}, Gate: &schema.Gate{Require: []string{"must"}}}
	for _, tc := range []struct {
		name    string
		steps   []*Step
		pass    bool
		missing int
	}{
		{"no receipts", nil, false, 1},
		{"only optional", []*Step{{Name: "advisory", Type: "script", Status: StatusPass}}, false, 1},
		{"unknown outcome", []*Step{{Name: "must", Type: "approval", Status: StepState(255)}}, false, 0},
		{"wrong type", []*Step{{Name: "must", Type: "script", Status: StatusPass}}, false, 1},
		{"duplicate", []*Step{{Name: "must", Type: "approval", Status: StatusPass}, {Name: "must", Type: "approval", Status: StatusPass}}, false, 1},
		{"optional failure", []*Step{{Name: "must", Type: "approval", Status: StatusPass}, {Name: "advisory", Type: "script", Status: StatusFail}}, true, 0},
		{"skipped", []*Step{{Name: "must", Type: "approval", Status: StatusSkipped}}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evaluateGate(&stage, nil, tc.steps, Placement{})
			if err != nil || got.Passed() != tc.pass || len(got.Missing) != tc.missing {
				t.Fatalf("gate=%+v err=%v", got, err)
			}
		})
	}
	stage.Gate = nil
	got, err := evaluateGate(&stage, nil, []*Step{{Name: "must", Type: "approval", Status: StatusPass}}, Placement{})
	if err != nil || got.Passed() || !reflect.DeepEqual(got.Missing, []string{"advisory"}) {
		t.Fatalf("default gate=%+v err=%v", got, err)
	}
}

func TestWorkflowStagePlanPinsExpandedChecks(t *testing.T) {
	t.Parallel()
	stage := schema.Stage{Name: "check", OnEnter: []schema.Step{{Name: "prepare", Uses: "script"}}, Steps: []schema.Step{{Name: "security", Uses: "scan", With: map[string]any{"on": "source", "policy": map[string]any{"severity": "high"}}}}, Gate: &schema.Gate{Require: []string{"security"}}}
	for _, scanners := range [][]Scanner{nil, {{"trivy", "source"}}, {{"trivy", "source"}, {"semgrep", "source"}, {"image", "image"}}} {
		plan, err := planStage(&stage, scanners)
		if err != nil {
			t.Fatal(err)
		}
		if !plan[0].onEnter || plan[1].onEnter {
			t.Fatal("entry/check ordering lost")
		}
		var checks []Check
		var outcomes []*Step
		for _, p := range plan {
			checks = append(checks, p.check)
			outcomes = append(outcomes, &Step{Name: p.check.Name, Type: p.check.Type, Status: StatusPass})
		}
		gate, err := evaluateGate(&stage, checks, outcomes, Placement{})
		if err != nil || !gate.Passed() {
			t.Fatalf("passed stage=%+v %v", gate, err)
		}
		outcomes[len(outcomes)-1].Status = StatusFail
		gate, err = evaluateGate(&stage, checks, outcomes, Placement{})
		if err != nil || gate.Passed() || len(gate.Blocking) != 1 {
			t.Fatalf("failed scanner=%+v %v", gate, err)
		}
		gate, err = evaluateGate(&stage, checks, outcomes[:len(outcomes)-1], Placement{})
		if err != nil || gate.Passed() || len(gate.Missing) != 1 {
			t.Fatalf("lost scanner=%+v %v", gate, err)
		}
		gate, err = evaluateGate(&stage, nil, outcomes, Placement{})
		if err != nil || gate.Passed() || len(gate.Missing) != 1 {
			t.Fatalf("missing expansion guessed from names: %+v %v", gate, err)
		}
	}
	plan, err := planStage(&stage, []Scanner{{"trivy", "source"}})
	if err != nil {
		t.Fatal(err)
	}
	stage.Steps[0].With["policy"].(map[string]any)["severity"] = "low"
	var config map[string]any
	if err := json.Unmarshal([]byte(plan[1].configJSON), &config); err != nil {
		t.Fatal(err)
	}
	if config["policy"].(map[string]any)["severity"] != "high" {
		t.Fatal("input mutation changed prepared configuration")
	}
	stage.Steps = append(stage.Steps, schema.Step{Name: "security:trivy", Uses: "approval"})
	if _, err := planStage(&stage, []Scanner{{"trivy", "source"}}); err == nil {
		t.Fatal("expanded check collision accepted")
	}
}

func TestWorkflowStageWithoutOptionsHasObjectConfiguration(t *testing.T) {
	t.Parallel()
	stage := &schema.Stage{Name: "check", OnEnter: []schema.Step{{Name: "prepare", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}
	plan, err := planStage(stage, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range plan {
		if step.configJSON != "{}" {
			t.Fatalf("%s has noncanonical configuration %s", step.check.Name, step.configJSON)
		}
	}
}

// With a check worker, a gate takes a passing check only from it (not from
// the agent worker, this machine, or a result of unknown placement) and never
// a script's result, whatever placement it records; an approval and a
// person's override keep their own meaning. Without one, every placement is
// taken as before.
func TestWithACheckWorkerTheGateTakesOnlyItsChecks(t *testing.T) {
	t.Parallel()
	check, agent := Placement{Worker: "https://check", Role: "check"}, Placement{Worker: "https://agent"}
	at := func(p Placement, kind string) json.RawMessage {
		ev, err := p.placed(kind, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	for name, tc := range map[string]struct {
		step *Step
		why  string
	}{
		"tests on the check worker":    {&Step{Type: "tests", Evidence: at(check, "tests")}, ""},
		"guidance on the check worker": {&Step{Type: "guidance/conformance", Evidence: at(check, "guidance/conformance")}, ""},
		"tests on the agent worker":    {&Step{Type: "tests", Evidence: at(agent, "tests")}, notIndependent},
		"tests on this machine":        {&Step{Type: "tests", Evidence: at(Placement{Local: true}, "tests")}, notIndependent},
		// Results recorded before placement was have none.
		"tests of unknown placement":       {&Step{Type: "tests", Evidence: json.RawMessage(`{}`)}, notIndependent},
		"tests of null placement":          {&Step{Type: "tests", Evidence: json.RawMessage(`{"placement":null}`)}, notIndependent},
		"tests of unreadable evidence":     {&Step{Type: "tests", Evidence: json.RawMessage(`{"placement":"https://check"}`)}, unreadable},
		"a script on the agent worker":     {&Step{Type: "script", Evidence: at(agent, "script")}, scriptNotCheck},
		"a script on this machine":         {&Step{Type: "script", Evidence: at(Placement{Local: true}, "script")}, scriptNotCheck},
		"a script of unknown placement":    {&Step{Type: "script", Evidence: json.RawMessage(`{}`)}, scriptNotCheck},
		"a script naming the check worker": {&Step{Type: "script", Evidence: at(check, "script")}, scriptNotCheck},
		"a script overridden":              {&Step{Type: "script", OverrideBy: "bob"}, ""},
		"an approval":                      {&Step{Type: "approval"}, ""},
	} {
		tc.step.Name, tc.step.Status = "must", StatusPass
		stage := schema.Stage{Name: "check", Steps: []schema.Step{{Name: "must", Uses: tc.step.Type}}}
		if got, err := evaluateGate(&stage, nil, []*Step{tc.step}, check); err != nil || !reflect.DeepEqual(got.Missing, map[bool][]string{true: {"must" + tc.why}}[tc.why != ""]) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		if got, err := evaluateGate(&stage, nil, []*Step{tc.step}, agent); err != nil || !got.Passed() {
			t.Errorf("%s without a check worker: %+v %v", name, got, err)
		}
	}
}
