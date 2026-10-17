package workflow

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/scm"

	"flokkit.io/forge/internal/forge/schema"
)

func TestAbandonWithoutRoleKeepsTheChange(t *testing.T) {
	t.Parallel()
	doc, err := schema.ParseWorkflow([]byte(`apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: research}
spec:
  kind: lifecycle
  stages:
    - name: s
      steps:
        - {name: accept, uses: approval, with: {means: "I looked"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	admissions, req, def := runAdmissionFixture(t)
	def.Spec = doc.Spec
	run, err := admissions.Create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	owner := NewAbandonments(admissions.records.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
	if _, err := owner.Abandon(context.Background(), Abandonment{RunID: run.ID, Who: "operator", Reason: "stopped"}); err != nil {
		t.Fatal(err)
	}
	wantChangeState(t, admissions.records.db, run.ChangeID, ChangeInProgress)
	wantRunState(t, admissions.records.db, run.ID, RunAbandoned)
}
