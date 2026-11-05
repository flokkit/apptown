package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// A deploy retry must retain the failed attempt, its exact backup evidence and
// attribution; reads of another project must not reveal it.
func TestLandingDeployHistoryRetainsOutcomesAcrossRetriesAndScopesProjects(t *testing.T) {
	t.Parallel()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "ship", Uses: "deploy", With: map[string]any{"deployment": "staff"}}}}}})
	steps, err := readSteps(context.Background(), q.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := newStepAttempts(q.db, func(k string) bool { return k == "deploy" })
	claim, err := owner.start(context.Background(), steps[0], "deploy", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	evidence := json.RawMessage(`{"reason":"no answer","failedStage":"checking","backup":{"outcome":"no-data","reason":"nothing to copy"}}`)
	if err := owner.finish(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: evidence, Details: json.RawMessage(`{"deployEvidence":{"reason":"spoof"}}`)}); err != nil {
		t.Fatal(err)
	}
	reader := Changes{db: q.db, authority: allowProject(run.ProjectID)}
	history, err := reader.LandingDeployAttempts(context.Background(), run.ProjectID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history %+v: %v", history, err)
	}
	if !history[0].OutcomeKnown || history[0].Step.Status != StatusFail || history[0].Step.OwnedAttempt() != 0 {
		t.Fatalf("outcome %+v", history[0])
	}
	assertDeployEvidence(t, history[0].Step.Evidence, evidence)
	// Reset through the same owner write as retry; the fixture isolates history
	// without dispatching a provider or requiring a person's retry authority.
	if _, err := workflowsql.New(q.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: claim.ID, Attempt: claim.Attempt, ConfigJson: claim.Config}); err != nil {
		t.Fatal(err)
	}
	history, err = reader.LandingDeployAttempts(context.Background(), run.ProjectID)
	if err != nil || len(history) != 1 || !history[0].OutcomeKnown || history[0].Step.Status != StatusFail {
		t.Fatalf("retry admission lost prior outcome: %+v %v", history, err)
	}
	assertDeployEvidence(t, history[0].Step.Evidence, evidence)
	current, err := readStep(context.Background(), q.db, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.start(context.Background(), current, "deploy", ids.Nil); err != nil {
		t.Fatal(err)
	}
	history, err = reader.LandingDeployAttempts(context.Background(), run.ProjectID)
	if err != nil || len(history) != 2 {
		t.Fatalf("retried history %+v: %v", history, err)
	}
	if history[0].Attempt != 2 || history[0].Step.Status != StatusRunning || history[1].Attempt != 1 || history[1].Step.Status != StatusFail {
		t.Fatalf("attempts %+v", history)
	}
	assertDeployEvidence(t, history[1].Step.Evidence, evidence)
	if _, err := reader.LandingDeployAttempts(context.Background(), ids.New(time.Now())); err == nil {
		t.Fatal("foreign project admitted")
	}
}
func assertDeployEvidence(t *testing.T, actual, expected json.RawMessage) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lost deploy evidence: %s; want %s", actual, expected)
	}
}
