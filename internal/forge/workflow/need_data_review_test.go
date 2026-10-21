package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/ids"
)

// A declaration is data, not a person's assent on retry.
func TestAWorkflowCannotDeclareUncopiedDataAcceptance(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "deploy", Uses: "deploy", With: map[string]any{"acceptUncopiedData": map[string]any{"by": "bls", "at": "2026-10-21T00:00:00Z"}}}}}}}
	doc := schema.Doc{APIVersion: "apptown.dev/v1", Kind: "Workflow", Metadata: schema.Metadata{Name: "review-assent"}, Spec: spec}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "acceptUncopiedData") {
		t.Fatalf("a workflow declaring an acceptance validated: %v", err)
	}
	// A definition stored before this rule is refused where its steps are
	// planned.
	if _, err := planStage(&spec.Stages[0], nil); err == nil || !strings.Contains(err.Error(), "acceptUncopiedData") {
		t.Fatalf("a declared acceptance was planned into a step: %v", err)
	}
}

// Resetting a deploy through an earlier entry step is a new attempt too.
func TestADeployResetThroughAnEarlierStepAcceptsNothing(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "script"}, {Name: "deploy", Uses: "deploy"}}}}}
	q, run := admitWorkflowSpec(t, spec)
	build := admittedStepID(t, q, run, "build")
	dep := admittedStepID(t, q, run, "deploy")
	attempts := newStepAttempts(q.db, func(string) bool { return true })
	finish := func(id ids.ID, state StepState) {
		t.Helper()
		s, err := readStep(t.Context(), q.db, id)
		if err != nil {
			t.Fatal(err)
		}
		c, err := attempts.Start(t.Context(), s, s.Type)
		if err != nil {
			t.Fatal(err)
		}
		if err := attempts.Finish(t.Context(), c, StepOutcome{Status: state, Evidence: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	finish(build, StatusPass)
	finish(dep, StatusError)
	retries := newStepRetries(q.db, []string{"script", "deploy"})
	if _, err := retries.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: dep, AcceptUncopiedData: true}, Actor: commandActor{Receipt: "bls"}}, nil); err != nil {
		t.Fatal(err)
	}
	finish(dep, StatusError)
	if _, err := retries.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: build}, Actor: commandActor{Receipt: "bls"}}, nil); err != nil {
		t.Fatal(err)
	}
	s := wantStepState(t, q.db, dep, StatusPending)
	if strings.Contains(string(s.Config), "acceptUncopiedData") {
		t.Fatalf("dependent deploy retains assent from its previous attempt: %s", s.Config)
	}
}
