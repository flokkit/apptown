package schema

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/stock"
)

func workflowWithStep(uses, with string) string {
	return "apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: w}\nspec:\n  kind: pipeline\n  stages:\n    - name: s\n      steps:\n        - name: one\n          uses: " + uses + "\n          with: " + with + "\n"
}

// A save refuses an option its step kind does not take, naming it, so an
// invented option (minApprovals for quorum) cannot save and then be dropped
// by the runner: Astra's surface review, 21 Oct, reproduced a two-person
// approval passing on one vote that way.
func TestASaveRefusesAnOptionTheStepKindDoesNotTake(t *testing.T) {
	t.Parallel()
	refused := map[string]string{
		"approval minApprovals":     workflowWithStep("approval", "{approvers: [alice, bob], means: Both checked it., minApprovals: 2}"),
		"approval cased Quorum":     workflowWithStep("approval", "{approvers: [alice, bob], means: Both checked it., Quorum: 2}"),
		"tests invented option":     workflowWithStep("tests", "{command: make test, retries: 3}"),
		"reconcile takes none":      workflowWithStep("reconcile", "{base: main}"),
		"retry mode is recorded":    workflowWithStep("agent/pi", "{prompt: do it, retryMode: continue}"),
		"empty scan is recorded":    workflowWithStep("scan", "{on: source, none: true}"),
		"deploy environment typo":   workflowWithStep("deploy", "{environment: main}"),
		"call by name not workflow": workflowWithStep("workflow/call", "{name: other}"),
	}
	for name, src := range refused {
		_, err := ParseWorkflow([]byte(src))
		if err == nil {
			t.Errorf("%s: saved", name)
			continue
		}
		if !strings.Contains(err.Error(), `step "one"`) {
			t.Errorf("%s: the refusal does not name the step: %v", name, err)
		}
	}
	if _, err := ParseWorkflow([]byte(refused["approval minApprovals"])); err == nil || !strings.Contains(err.Error(), "minApprovals") || !strings.Contains(err.Error(), "quorum") {
		t.Fatalf("the refusal names the key and the kind's options: %v", err)
	}
}

// A stored workflow was accepted under the rules of its day and stays
// readable and runnable.
func TestAStoredWorkflowWithAnUnknownOptionStaysReadable(t *testing.T) {
	t.Parallel()
	if _, err := ParseWorkflowStored([]byte(workflowWithStep("tests", "{command: make test, retries: 3}"))); err != nil {
		t.Fatal(err)
	}
}

// Where an option's value is data, its contents are not options: a script's
// environment and a called workflow's inputs take any names.
func TestDataValuedOptionsTakeAnyKeys(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		workflowWithStep("script", "{run: make, env: {ANY_NAME: x, other: y}}"),
		workflowWithStep("workflow/call", "{workflow: other, with: {anything: x}}"),
	} {
		if _, err := ParseWorkflow([]byte(src)); err != nil {
			t.Error(err)
		}
	}
}

// Every kind AppTown runs says which options it takes, and only those
// kinds do.
func TestEveryStepKindDeclaresItsOptions(t *testing.T) {
	t.Parallel()
	kinds := slices.Sorted(maps.Keys(SupportedSteps))
	declared := slices.Sorted(maps.Keys(stepOptions))
	if !slices.Equal(kinds, declared) {
		t.Fatalf("step kinds %v, kinds with declared options %v", kinds, declared)
	}
	for kind, recorded := range writtenByAppTown {
		for _, k := range recorded {
			if slices.Contains(stepOptions[kind], k) {
				t.Errorf("%s: %s is both authored and recorded by AppTown", kind, k)
			}
		}
	}
}

// Every workflow AppTown ships saves under the rule.
func TestEveryStockWorkflowSavesUnderTheOptionRule(t *testing.T) {
	t.Parallel()
	names, err := fs.Glob(stock.FS, "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range names {
		raw, err := fs.ReadFile(stock.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "\nkind: Workflow") {
			continue
		}
		n++
		if _, err := ParseWorkflow(raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n == 0 {
		t.Fatal("no stock workflows found")
	}
}
