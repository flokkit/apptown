package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

func TestANewCandidateNeedsFreshApproval(t *testing.T) {
	ctx := context.Background()
	db, change := changeEditFixture(t, ChangeDraft{Title: "landing"})
	automatic := func(kind string) bool { return kind == "reconcile" || kind == "tests" }
	admissions := newRunAdmissions(db, NewStages(nil, automatic), nil)
	run, err := admissions.Create(ctx, StartRequest{ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "land", InputsJSON: "{}", requestedBy: "operator"}, PreparedRun{
		Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{
			{Name: "catch-up", Uses: "reconcile"}, {Name: "tests", Uses: "tests"}, {Name: "approve", Uses: "approval", With: map[string]any{"means": "I approve this source"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(db, automatic)
	start := func(name string) *Step {
		t.Helper()
		snapshot, err := readStep(ctx, db, admittedStepID(t, queries{db: db}, run, name))
		if err != nil {
			t.Fatal(err)
		}
		claim, err := attempts.Start(ctx, snapshot, snapshot.Type)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	finish := func(claim *Step, candidate string) {
		t.Helper()
		ev, _ := json.Marshal(map[string]string{"candidate": candidate})
		if err := attempts.Finish(ctx, claim, StepOutcome{Status: StatusPass, Evidence: ev}); err != nil {
			t.Fatal(err)
		}
	}
	pin := func(candidate string) {
		t.Helper()
		claim := start("catch-up")
		e := &Execution{service: &Service{db: db}, ctx: ctx, step: claim, run: run, claim: claim}
		if _, err := e.PinLandingCandidate(LandingCandidate{Candidate: candidate, Target: "main"}); err != nil {
			t.Fatal(err)
		}
		finish(claim, candidate)
	}
	pin("candidate-X")
	finish(start("tests"), "candidate-X")
	approval := admittedStepID(t, queries{db: db}, run, "approve")
	service := newService(db, allowProject(run.ProjectID), Runtime{})
	if _, err := service.Approve(ctx, Approval{StepID: approval, Decision: VoteApprove, Comment: "reviewed X"}); err != nil {
		t.Fatal(err)
	}
	if _, err := invalidateAndRetryForTest(ctx, db, run, "catch-up"); err != nil {
		t.Fatal(err)
	}
	pin("candidate-Y")
	finish(start("tests"), "candidate-Y")
	saved, err := readStep(ctx, db, approval)
	if err != nil {
		t.Fatal(err)
	}
	reason, err := landingCandidateRefusal(ctx, db, run.ID, LandingCandidate{Candidate: "candidate-Y", Target: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status == StatusPass && reason == "" {
		t.Fatalf("candidate Y retained X's approval and passes candidate admission: %s", saved.Evidence)
	}
}
