package httpapi

import (
	"strings"
	"testing"
)

func TestWorkflowSummary(t *testing.T) {
	t.Parallel()
	sum := summarizeWorkflow(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: build, description: builds things}
spec:
  kind: lifecycle
  stages:
    - name: build
      steps:
        - {name: work, uses: agent/pi}
        - {name: accept, uses: approval, with: {approvers: [bls]}}
`)
	if sum == nil {
		t.Fatal("valid workflow must summarize")
	}
	if sum.Pacing != "paced by people" {
		t.Fatalf("lifecycle pacing: %q", sum.Pacing)
	}
	if !strings.Contains(sum.Flow, "work") || !strings.Contains(sum.Flow, "accept") {
		t.Fatalf("flow line: %q", sum.Flow)
	}
	steps := sum.Stages[0].Steps
	if !steps[1].Human || !strings.Contains(steps[1].Gloss, "bls") {
		t.Fatalf("approval must be a human moment naming approvers: %+v", steps[1])
	}
	if !strings.Contains(sum.Stages[0].GateText, "finishes") {
		t.Fatalf("last stage should say the run finishes: %q", sum.Stages[0].GateText)
	}
	if summarizeWorkflow("not: yaml: [") != nil {
		t.Fatal("unparseable yaml must summarize to nil")
	}
}

// The plain reading speaks the approval's full shape: quorum, groups in
// ordinary words, and the standard of approval.
func TestSummaryApprovalGloss(t *testing.T) {
	t.Parallel()
	sum := summarizeWorkflow(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: { name: g }
spec:
  kind: lifecycle
  stages:
    - name: work
      steps:
        - name: panel
          uses: approval
          with:
            approvers: ["bls", "group:parents"]
            quorum: 2
            means: "we both read the diff"
`)
	if sum == nil {
		t.Fatal("summary refused a stored workflow")
	}
	step := sum.Stages[0].Steps[0]
	if step.Gloss != "waits for OKs from 2 people (bls or the parents group)" {
		t.Fatalf("quorum gloss: %q", step.Gloss)
	}
	if step.Means != "we both read the diff" {
		t.Fatalf("means missing from the reading: %q", step.Means)
	}

	// Pre-rule stored documents still read (prospective law).
	old := summarizeWorkflow(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: { name: old }
spec:
  kind: lifecycle
  stages:
    - name: work
      steps:
        - name: ok
          uses: approval
`)
	if old == nil || old.Stages[0].Steps[0].Gloss != "waits for a person's OK" {
		t.Fatalf("stored pre-means workflow must still read: %+v", old)
	}
}

// An agent step's prompt is addressed to the agent; the plain reading never
// quotes it to the person reading the workflow.
func TestSummaryAgentGlossDoesNotQuoteThePrompt(t *testing.T) {
	t.Parallel()
	sum := summarizeWorkflow(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: { name: g }
spec:
  kind: lifecycle
  stages:
    - name: work
      steps:
        - name: apply-change
          uses: agent/pi
          with:
            prompt: |
              The current working directory is this project's repository.
              Apply the change below.
`)
	if sum == nil {
		t.Fatal("summary refused a stored workflow")
	}
	step := sum.Stages[0].Steps[0]
	if strings.Contains(step.Gloss, "working directory") || step.Gloss != "an agent works on it" {
		t.Fatalf("agent gloss quotes the prompt: %q", step.Gloss)
	}
}
