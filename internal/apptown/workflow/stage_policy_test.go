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
			got, err := evaluateGate(&stage, nil, tc.steps)
			if err != nil || got.Passed() != tc.pass || len(got.Missing) != tc.missing {
				t.Fatalf("gate=%+v err=%v", got, err)
			}
		})
	}
	stage.Gate = nil
	got, err := evaluateGate(&stage, nil, []*Step{{Name: "must", Type: "approval", Status: StatusPass}})
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
		gate, err := evaluateGate(&stage, checks, outcomes)
		if err != nil || !gate.Passed() {
			t.Fatalf("passed stage=%+v %v", gate, err)
		}
		outcomes[len(outcomes)-1].Status = StatusFail
		gate, err = evaluateGate(&stage, checks, outcomes)
		if err != nil || gate.Passed() || len(gate.Blocking) != 1 {
			t.Fatalf("failed scanner=%+v %v", gate, err)
		}
		gate, err = evaluateGate(&stage, checks, outcomes[:len(outcomes)-1])
		if err != nil || gate.Passed() || len(gate.Missing) != 1 {
			t.Fatalf("lost scanner=%+v %v", gate, err)
		}
		gate, err = evaluateGate(&stage, nil, outcomes)
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
