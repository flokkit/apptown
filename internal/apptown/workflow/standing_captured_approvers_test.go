package workflow

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestPersonStandingCarryKeepsCapturedLandingApprovers(t *testing.T) {
	t.Parallel()
	a, rules, _, _, _ := standingFixture(t)
	for _, spec := range []identity.Spec{
		{SubjectType: "user", Subject: "owner", Capability: string(access.CapInstanceStandingApproval), Scope: access.ScopeInstance, By: "boss"},
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), By: "boss"},
		{SubjectType: "user", Subject: "owner", Capability: string(access.CapChangeLand), Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), By: "boss"},
	} {
		if _, err := a.Access.Grant(t.Context(), spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "owner"))
	portfolio := &standingPortfolioFixture{}
	rules.deps.Portfolio = portfolio
	in := JanitorChoices{Dependencies: "routine"}
	rule := enableStanding(t, rules, ctx, in)
	authority := &standingVote{Level: "routine", RuleID: rule.RuleID, Revision: rule.Revision, Issuer: rule.Issuer, Means: "accept bounded routine updates after all checks pass", AssessmentDigest: "checked-source"}
	work, err := readRun(ctx, a.DB, a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := readStep(ctx, a.DB, a.StepID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Transact(ctx, a.DB, func(tx *database.Tx) (reworkedResult, error) {
		return (reworkStore{db: a.DB}).approveIn(ctx, tx, work, accept, ApprovalVote{Approver: "the janitor", Principal: rule.Issuer.Principal, Via: "standing", Decision: VoteApprove, Standing: authority, AlsoApproves: []string{"land this qualified candidate"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) error { return nil })
	if err := (advanceStore{db: a.DB, stages: a.Work.stages, completion: completion}).complete(ctx, work); err != nil {
		t.Fatal(err)
	}
	// Model a captured landing that still names another person, while the
	// current procedure permits role:project-approver. The meaning is unchanged.
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "checks", Steps: []schema.Step{{Name: "tests", Uses: "tests"}}},
		{Name: "approval", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"another-person"}, "means": "I approve the landing", "standing": map[string]any{"class": "routine-update", "means": "land this qualified candidate"}}}}},
		{Name: "ship", Steps: []schema.Step{{Name: "record", Uses: "script"}}},
	}}
	admissions := newRunAdmissions(a.DB, NewStages(nil, func(string) bool { return false }), nil)
	landing, err := admissions.records.create(ctx, StartRequest{ProjectID: a.ProjectID, ChangeID: a.ChangeID, WorkflowName: "land", InputsJSON: "{}", acceptedRunID: work.ID, requestedBy: "apptown-system"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.records.prepared(ctx, landing.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := readSteps(ctx, a.DB, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := (newStepAttempts(a.DB, func(string) bool { return true })).start(ctx, steps[0], "tests", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "landing-check", Verdict: VerdictPass, Preexisting: true}}}).Outcome(StatusPass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (newStepAttempts(a.DB, func(string) bool { return true })).finish(ctx, attempt, outcome); err != nil {
		t.Fatal(err)
	}
	current, err := readRun(ctx, a.DB, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (advanceStore{db: a.DB, stages: NewStages(nil, func(string) bool { return false })}).transition(ctx, current, "approval", "apptown-system"); err != nil {
		t.Fatal(err)
	}
	current, err = readRun(ctx, a.DB, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := landings{db: a.DB, standing: &rules, eligible: (commandAuthority{db: a.DB, checked: a.Authorizer}).mayApprove}
	if _, err := store.apply(ctx, work.ID, current.ID); err != nil {
		t.Fatal(err)
	}
	if why, err := rules.checkStandingLanding(ctx, a.DB, current, authority); err != nil || why != "" {
		t.Fatalf("current procedure must permit issuer: %q %v", why, err)
	}

	cast, err := store.carry(ctx, current)
	if err != nil || cast {
		t.Fatalf("captured landing exclusion was bypassed: carried=%t %v", cast, err)
	}
}
