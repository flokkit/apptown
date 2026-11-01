package workflow

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

// Admit the declaration the test needs, including its real initial steps and
// receipts. No dispatcher runs: tests drive the operation they are exercising.
func admitWorkflowSpec(t *testing.T, spec schema.Spec) (queries, *Run) {
	t.Helper()
	db, change := changeEditFixture(t, ChangeDraft{Title: "A change"})
	admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool {
		return kind == "script" || kind == "scan" || kind == "agent/pi" || kind == "agent/review"
	}), nil)
	run, err := admissions.records.create(context.Background(), StartRequest{
		ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "test", InputsJSON: "{}", requestedBy: "operator",
	}, PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.records.prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	return queries{db: db}, run
}

// The read selects a setup identity only; persistence assertions stay in SQL.
func admittedStepID(t *testing.T, q queries, run *Run, name string) ids.ID {
	t.Helper()
	steps, err := readStageSteps(context.Background(), q.db, run.ID, run.CurrentStage)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Name == name {
			return step.ID
		}
	}
	t.Fatalf("admitted run %d has no step %q", run.ID, name)
	return ids.Nil
}

func admitSingleStep(t *testing.T, step schema.Step) (queries, *Run, ids.ID) {
	t.Helper()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{step}}}})
	return q, run, admittedStepID(t, q, run, step.Name)
}
