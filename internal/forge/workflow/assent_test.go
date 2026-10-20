package workflow

import (
	"io/fs"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/stock"
)

// Every shipped workflow is judged by what it declares: the landing that
// waits for a person (land) and every working or record lane need no
// landing authority; ship, which merges and deploys with no approval, does.
// A new stock workflow must be placed in one list or the other.
func TestStockWorkflowsAreJudgedByTheirDeclaredEffects(t *testing.T) {
	needsLanding := map[string]bool{"ship": true}
	names, err := fs.Glob(stock.FS, "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, name := range names {
		raw, err := fs.ReadFile(stock.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := schema.ParseWorkflowStored(raw)
		if err != nil || doc.Kind != "Workflow" {
			continue // packs.yaml, policies.yaml and the like
		}
		seen++
		effect, err := unassentedEffect(&doc.Spec, false, stockCallee(t), 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wf := strings.TrimSuffix(name, ".yaml")
		if (effect != "") != needsLanding[wf] {
			t.Errorf("%s: unassented effect %q, needs landing authority %v", wf, effect, needsLanding[wf])
		}
	}
	if seen < 10 {
		t.Fatalf("only %d stock workflows read", seen)
	}
}

// The rule reads order, not names: an approval protects only what comes in
// a later stage (or a merge after its own stage).
func TestUnassentedEffectReadsStageOrder(t *testing.T) {
	approval := schema.Step{Name: "accept", Uses: "approval"}
	deploy := schema.Step{Name: "deploy", Uses: "deploy"}
	call := schema.Step{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "callee"}}
	spec := func(s schema.Spec) calleeSpec { return func(string) (*schema.Spec, error) { return &s, nil } }
	deploys := spec(schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "x", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}}})
	quiet := spec(schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "x", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}})
	childLanding := spec(schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "x", Steps: []schema.Step{{Name: "work", Uses: "script"}}}, {Name: "y", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}})
	missing := func(string) (*schema.Spec, error) { return nil, fs.ErrNotExist }
	work := schema.Step{Name: "work", Uses: "script"}
	cases := []struct {
		name   string
		spec   schema.Spec
		effect string
		callee calleeSpec
	}{
		{"a working lane with no effect", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work}}}}, "", nil},
		{"a landing that merges with no approval", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work}}, {Name: "b", Steps: []schema.Step{work}}}}, "merge", nil},
		{"a landing approved before it merges", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{approval}}, {Name: "b", Steps: []schema.Step{work}}}}, "", nil},
		{"a one-stage landing approved in its stage", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work, approval}}}}, "", nil},
		{"an approval the gate does not require does not count", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work, approval}, Gate: &schema.Gate{Require: []string{"work"}}}}}, "merge", nil},
		{"an approval the gate requires counts", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work, approval}, Gate: &schema.Gate{Require: []string{"work", "accept"}}}}}, "", nil},
		{"approval after the merge does not count", schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{work}}, {Name: "b", Steps: []schema.Step{approval}}}}, "merge", nil},
		{"a working lane that deploys", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", OnEnter: []schema.Step{deploy}, Steps: []schema.Step{work}}}}, "deploy", nil},
		{"a deploy in the approval's own stage", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{approval, deploy}}}}, "deploy", nil},
		{"a deploy after approval", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{approval}}, {Name: "b", Steps: []schema.Step{deploy}}}}, "", nil},
		{"a call is judged by its callee: one that deploys", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{call}}}}, "deploy", deploys},
		{"a call to a callee with no effect", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{call}}}}, "", quiet},
		{"a callee that cannot be read is an effect", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{call}}}}, "could not be read", missing},
		{"a call after approval", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{approval}}, {Name: "b", Steps: []schema.Step{call}}}}, "", deploys},
		{"a called landing does not merge as a child", schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "a", Steps: []schema.Step{call}}}}, "", childLanding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unassentedEffect(&tc.spec, false, tc.callee, 0)
			if err != nil || (tc.effect == "") != (got == "") || !strings.Contains(got, tc.effect) {
				t.Fatalf("got %q %v, want %q", got, err, tc.effect)
			}
		})
	}
}

// stockCallee resolves a called stock workflow by name.
func stockCallee(t *testing.T) calleeSpec {
	return func(name string) (*schema.Spec, error) {
		raw, err := fs.ReadFile(stock.FS, name+".yaml")
		if err != nil {
			return nil, err
		}
		doc, err := schema.ParseWorkflowStored(raw)
		if err != nil {
			return nil, err
		}
		return &doc.Spec, nil
	}
}

// Only a person's decision is assent: AppTown itself, a step run or a chat
// recorded as approver is not, and neither is a pending or failed approval.
func TestOnlyAPersonsApprovalIsAssent(t *testing.T) {
	step := func(status StepState, approver, override string) *Step {
		evidence := []byte(`{"decisions":[],"quorum":1,"approvals":1,"approver":"` + approver + `"}`)
		return &Step{Type: "approval", Status: status, Evidence: evidence, OverrideBy: override}
	}
	cases := []struct {
		name string
		step *Step
		want bool
	}{
		{"a person's approval", step(StatusPass, "bls", ""), true},
		{"a person's override", step(StatusPass, "", "bls"), true},
		{"AppTown itself", step(StatusPass, "apptown-system", ""), false},
		{"a step run", step(StatusPass, "run/3", ""), false},
		{"a chat", step(StatusPass, "bls via console/x", ""), false},
		{"pending", step(StatusPending, "bls", ""), false},
		{"not an approval", &Step{Type: "script", Status: StatusPass}, false},
	}
	for _, tc := range cases {
		if got := approvedByAPerson(tc.step); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
