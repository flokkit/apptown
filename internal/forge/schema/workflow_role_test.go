package schema

import (
	"testing"
)

// A workflow with no role is a working run — never the old fused kind
// whose abandonment closed the change and removed the worktree.
func TestOmittedRoleIsWorking(t *testing.T) {
	t.Parallel()
	doc, err := ParseWorkflow([]byte(`apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: bare}
spec:
  kind: lifecycle
  stages:
    - name: s
      steps:
        - {name: ok, uses: approval, with: {means: "I looked"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Spec.Role != "working" {
		t.Fatalf("omitted role must default to working, got %q", doc.Spec.Role)
	}
	if _, err := ParseWorkflow([]byte("apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: x}\nspec: {kind: lifecycle, role: fused, stages: [{name: s, steps: [{name: ok, uses: approval, with: {means: \"I looked\"}}]}]}\n")); err == nil {
		t.Fatal("an unknown role must be refused")
	}
}

// A record workflow (research, a specification) is a role of its own: its
// outcome is the records it keeps, and its acceptance lands nothing.
func TestARecordRoleIsAccepted(t *testing.T) {
	t.Parallel()
	doc, err := ParseWorkflow([]byte("apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: r}\nspec: {kind: lifecycle, role: record, stages: [{name: s, steps: [{name: ok, uses: approval, with: {means: \"I looked\"}}]}]}\n"))
	if err != nil || doc.Spec.Role != "record" {
		t.Fatalf("a record workflow: %+v %v", doc, err)
	}
}
