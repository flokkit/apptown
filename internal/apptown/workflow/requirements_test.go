package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/dbtest"
)

// The invoke-time half of the requires contract: a workflow whose step
// requires an artifact kind the project doesn't have must be caught BEFORE a
// run exists (a run that dies at dispatch sits "running" with failed gates
// and nobody is told). A kind produced by an earlier step of the same
// document is not missing.
func TestMissingRequiredArtifact(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	owner := newRunAdmissions(db, NewStages(nil, func(string) bool { return true }), nil)
	project := workflowTestProject(t, db, "p", "P", "")
	doc, err := schema.ParseWorkflowStored([]byte(`
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: t, category: change }
spec:
  kind: lifecycle
  stages:
    - name: build
      onEnter:
        - name: build-v1
          uses: agent/pi
          with:
            requires: [spec-final]
            output: { kind: build-report, name: build-report, file: R.md }
    - name: verify
      onEnter:
        - name: check
          uses: agent/pi
          with:
            requires: [build-report]
`))
	if err != nil {
		t.Fatal(err)
	}
	// No spec-final anywhere → missing. build-report is produced by the
	// earlier step, so it must NOT be reported even though it doesn't exist.
	if got, err := owner.MissingArtifact(context.Background(), project, &doc.Spec); err != nil || got != "spec-final" {
		t.Fatalf("want spec-final missing, got %q", got)
	}

	// With a spec-final artifact in the project, nothing is missing.
	producer, err := owner.Create(context.Background(), StartRequest{ProjectID: project, WorkflowName: "product-spec", InputsJSON: `{}`, RequestedBy: "operator"}, PreparedRun{
		Workflow: DefinitionRef{Name: "product-spec", Version: 1},
		Spec:     schema.Spec{Kind: "change", Stages: []schema.Stage{{Name: "produce"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (advanceStore{db: db, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), producer); err != nil {
		t.Fatal(err)
	}
	if _, err := AddArtifactIn(context.Background(), db, producer.ID, "spec-final", "spec", "", "the spec", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := owner.MissingArtifact(context.Background(), project, &doc.Spec); err != nil || got != "" {
		t.Fatalf("spec-final exists — nothing should be missing, got %q", got)
	}
}

func TestArtifactRequirementsAreScopedOrderedAndReadFailuresPropagate(t *testing.T) {
	t.Parallel()
	owner, req, def := runAdmissionFixture(t)
	db := owner.records.db
	foreign := workflowTestProject(t, db, "foreign", "Foreign", "")
	foreignChange := admissionTestChange(t, db, foreign, "foreign")
	run, err := owner.Create(context.Background(), StartRequest{ProjectID: foreign, ChangeID: foreignChange, WorkflowName: "foreign", InputsJSON: `{}`, RequestedBy: "operator"}, def)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddArtifactIn(context.Background(), db, run.ID, "report", "report", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	def.Spec.Stages[0].Steps = []schema.Step{
		{Name: "needs", With: map[string]any{"requires": []string{"report", "report"}}},
		{Name: "produces-later", With: map[string]any{"output": map[string]string{"kind": "report"}}},
	}
	if got, err := owner.MissingArtifact(context.Background(), req.ProjectID, &def.Spec); err != nil || got != "report" {
		t.Fatalf("foreign/later artifact satisfied early requirement: %q %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "SELECT DISTINCT a.kind")
	if got, err := owner.MissingArtifact(context.Background(), req.ProjectID, &def.Spec); err == nil || got != "" {
		t.Fatalf("failed read became missing artifact: %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.MissingArtifact(ctx, req.ProjectID, &def.Spec); err == nil {
		t.Fatal("artifact lookup ignored cancellation")
	}
}
