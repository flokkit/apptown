package apptown

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

// Each test here reaches a merge or a deploy by a door the start-time check
// does not cover, or covers wrongly, and each is stopped by the check at the
// effect (workflow/assent.go): a merge or deploy needs a person's approval in the
// run, or a start by someone who may land.

// An approval the stage's gate does not require is not assent.
func TestAnOptionalApprovalDoesNotLetAnAgentMerge(t *testing.T) {
	t.Parallel()
	a, agent, ch, before := shipFixture(t)
	seedLibraryWorkflow(t, a, ch.ProjectID, "optional-assent", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: optional-assent}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: check
      steps:
        - {name: scan, uses: scan, with: {on: source}}
        - {name: accept, uses: approval, with: {means: "I accept this landing"}}
      gate: {require: [scan]}
`)
	bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"optional-assent"}`)
	driveChange(t, a, ch.ID)
	if after := defaultTip(t, a); after != before {
		t.Fatalf("an agent merged past an optional approval: %s -> %s", before, after)
	}
}

// Completed work handed to a landing is not a person's acceptance
// unless a person approved it; a landing with no approval of its own then
// waits at the merge.
func TestCompletedWorkIsNotAPersonsAcceptance(t *testing.T) {
	t.Parallel()
	a, agent, ch, before := shipFixture(t)
	seedLibraryWorkflow(t, a, ch.ProjectID, "quiet-work", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: quiet-work}
spec:
  kind: pipeline
  role: working
  stages:
    - name: check
      steps:
        - {name: scan, uses: scan, with: {on: source}}
`)
	if err := a.Ways.BindSystemWorkflow(t.Context(), "land", "ship", "fixture"); err != nil {
		t.Fatal(err)
	}
	if w := bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"quiet-work"}`); w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	a.Work.Wait()
	runs, err := a.Work.ChangeRuns(a.asEngine(context.Background()), ch.ID, workflow.ChangeRuns{})
	if err != nil {
		t.Fatal(err)
	}
	handed := false
	for _, run := range runs {
		if run.WorkflowName == "quiet-work" {
			// The completion job's own call; tests run no job worker.
			if _, err := a.Work.AdvanceAcceptedLanding(a.asEngine(context.Background()), run.ID, workcommands.AcceptedLandings{DB: a.DB, Work: a.Work, Lanes: a.lifecycle}); err != nil {
				t.Logf("handoff: %v", err)
			}
			handed = true
		}
	}
	if !handed {
		t.Fatal("the working run did not complete")
	}
	driveChange(t, a, ch.ID)
	if after := defaultTip(t, a); after != before {
		t.Fatalf("unapproved work landed: %s -> %s", before, after)
	}
}

// Remediation admits its run without the start-time check; the
// AppTown's own instrument is no person, so its deploy waits.
func TestARemediationRunDoesNotDeployWithoutAPerson(t *testing.T) {
	t.Parallel()
	a, _, item, def := remediationAttemptFixture(t, false)
	installFakeGit(t, a, nil)
	deployed := false
	fixture := installDispatchFixture(t, a)
	fixture.runners["deploy"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			return // refused at the effect
		}
		deployed = true
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
			t.Error(err)
		}
	}
	def.Run.Spec = schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "deploy", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}}}
	run, err := remediationAttemptsFor(a).Start(context.Background(), item, def, "review-worker")
	if err != nil {
		t.Fatalf("remediation start: %v", err)
	}
	if err := a.Work.DispatchStarted(run.ID); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	if deployed {
		t.Fatal("AppTown's own remediation deployed with no person")
	}
	requireStepWaitingOnAPerson(t, a, run.ID, "deploy")
}

// A send-back is a fresh root; the person's start of the first run
// authorizes nothing in it. An agent's send-back reaches the deploy and
// waits there.
func TestAnAgentsSendBackDoesNotRedeploy(t *testing.T) {
	t.Parallel()
	a, agent, ch, _ := shipFixture(t)
	seedLibraryWorkflow(t, a, ch.ProjectID, "work-and-deploy", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: work-and-deploy}
spec:
  kind: pipeline
  role: working
  stages:
    - name: work
      onEnter:
        - {name: edit, uses: agent/pi, with: {prompt: "Make the requested edit"}}
      steps:
        - {name: publish, uses: deploy, with: {deployment: main}}
`)
	deploys := 0
	fixture := installDispatchFixture(t, a)
	fixture.runners["agent/pi"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
			t.Error(err)
		}
	}
	fixture.runners["deploy"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			return // refused at the effect
		}
		deploys++
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
			t.Error(err)
		}
	}
	cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
	if w := bearerCall(a, cli, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"work-and-deploy"}`); w.Code != 201 {
		t.Fatalf("the person's start: %d %s", w.Code, w.Body.String())
	}
	a.Work.Wait()
	if deploys != 1 {
		t.Fatalf("the person's run deploys once: %d", deploys)
	}
	w := bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/sendback", ch.ID), `{"note":"make a different change"}`)
	if w.Code >= 300 {
		t.Fatalf("send-back: %d %s", w.Code, w.Body.String())
	}
	a.Work.Wait()
	if deploys != 1 {
		t.Fatalf("the agent's send-back redeployed: %d", deploys)
	}
}

// A run admitted before this rule carries no landing fact (the
// migration records none), so it reaches its merge only with a person's
// approval, also after a restart.
func TestARunAdmittedBeforeTheRuleDoesNotLandOnRecovery(t *testing.T) {
	t.Parallel()
	a, _, ch, before := shipFixture(t)
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "ship"})
	if run == nil || err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("setup: %+v %v", run, err)
	}
	// sql-guarantee: a run admitted before landing authority was recorded; no operation makes one now.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE runs SET landing_authority='' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.ResumePending()
	driveChange(t, a, ch.ID)
	if after := defaultTip(t, a); after != before {
		t.Fatalf("recovery landed a run no person authorized: %s -> %s", before, after)
	}
}

// At the effect itself: a landing with neither fact is refused at the merge
// with the plain reason and main stays put; once a person approves in the
// run, the same merge goes ahead.
func TestTheMergeWaitsForAPersonsApproval(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	p := engineProject(t, a, "p")
	ch := engineLandingChange(t, a, p)
	run := engineLandingRun(t, a, p, ch)
	// sql-guarantee: the same run without its landing fact, as one admitted by an agent's request.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE runs SET landing_authority='' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	observed, err := mergeEffectForTest(t, a, mergeSourceForTest(t, a, run))
	if err != nil || observed.Merged || !strings.Contains(observed.Refusal, "no person has approved this run") {
		t.Fatalf("an unapproved merge: %+v %v", observed, err)
	}
	snap, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == "hold" {
			if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: s.ID, Decision: workflow.VoteApprove}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if merged, _, _ := mergeRunForTest(t, a, run); !merged {
		t.Fatal("an approved landing did not merge")
	}
}

// requireStepWaitingOnAPerson: the refused effect is recorded on its step,
// waiting on a person, with the reason.
func requireStepWaitingOnAPerson(t *testing.T, a *App, runID ids.ID, name string) {
	t.Helper()
	snap, err := a.Work.Snapshot(a.asEngine(context.Background()), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == name {
			if s.Status != workflow.StatusNeedsHuman || !strings.Contains(string(s.Evidence), "no person has approved this run") {
				t.Fatalf("%s: %s %s", name, s.Status, s.Evidence)
			}
			return
		}
	}
	t.Fatalf("no step %q", name)
}

// driveChange advances the change's running runs as the engine would until
// nothing moves: tests run no job worker for advancement.
func driveChange(t *testing.T, a *App, changeID ids.ID) {
	t.Helper()
	for i := 0; i < 6; i++ {
		a.Work.Wait()
		runs, err := a.Work.ChangeRuns(a.asEngine(context.Background()), changeID, workflow.ChangeRuns{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Status == workflow.RunRunning {
				_, _ = advanceAs(a, run.ID, "system")
			}
		}
	}
	a.Work.Wait()
}

// A landing on a project without a repository merges nothing, but the
// stage's required checks still decide whether it may complete: they were
// skipped for such projects, so a landing completed with its checks pending.
func TestALandingWithoutARepositoryStillWaitsForItsChecks(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := engineProject(t, a, "no-repo")
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "ready", Steps: []schema.Step{{Name: "check", Uses: "script", With: map[string]any{"script": "true"}}}},
		{Name: "landed", Steps: []schema.Step{{Name: "record", Uses: "script", With: map[string]any{"script": "true"}}}},
	}}})
	a.lifecycle.SelectLane(t.Context(), p.ID, "land", true, "fixture")
	ch := engineChange(t, a, p)
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if run == nil || err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("setup: %+v %v", run, err)
	}
	observed, err := mergeEffectForTest(t, a, mergeSourceForTest(t, a, run))
	if err != nil || observed.Merged || !strings.Contains(observed.Refusal, "waiting for required checks before merging: check") {
		t.Fatalf("a landing without a repository skipped its checks: %+v %v", observed, err)
	}
}
