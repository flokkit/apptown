package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/projectlifecycle"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestSupersedeWedgedLanding(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)

	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "test", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "test", "fixture")
	landYAML := `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: land, description: d}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: gates
      steps:
        - {name: scanit, uses: scan, with: {scanner: osv}}
`
	workYAML := `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: work, description: d}
spec:
  kind: lifecycle
  role: working
  stages:
    - name: apply
      steps:
        - {name: ok, uses: approval, with: {means: "the service change is ready"}}
`
	for _, w := range []struct{ name, yaml string }{{"land", landYAML}, {"work", workYAML}} {
		seedLibraryWorkflow(t, a, p.ID, w.name, w.yaml)
	}
	ctx := a.asForge(context.Background(), p.ID, "test")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "c"})
	if err != nil {
		t.Fatal(err)
	}

	startLanding := func(status workflow.StepState) *workflow.Run {
		t.Helper()
		a.Work.Stop()
		run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "land"})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Steps) != 1 || snapshot.Steps[0].Type != "scan" {
			t.Fatalf("landing scan step: %+v", snapshot.Steps)
		}
		if status == workflow.StatusFail {
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), snapshot.Steps[0].ID); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"scan"}
		deps.Report = func(err error) { t.Error(err) }
		deps.Runners = map[string]workflow.Runner{"scan": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"wedged gate"}`)}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	landing := startLanding(workflow.StatusFail)

	// Wedged landing (failed gate, nothing queued) → superseded.
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO audit_log")
	if _, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "work"}); err == nil {
		t.Fatal("must not replace a landing when abandonment fails")
	}
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.Status == workflow.RunRunning })
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == landing.ID && r.Status == workflow.RunRunning })
	requireEvents(t, a, 0, eventKind("run.abandoned"))
	work, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("expected supersede, got error: %v", err)
	}
	settled, err := workflow.ReadRunIn(context.Background(), db, landing.ID)
	if err != nil || settled.Status != workflow.RunAbandoned {
		t.Fatalf("wedged landing not abandoned: %+v %v", settled, err)
	}
	returned, err := a.changeByID(ctx, change.ID)
	if err != nil || returned.State != workflow.ChangeInProgress {
		t.Fatalf("change not returned to work: %+v %v", returned, err)
	}
	snapshot, err := a.Work.Snapshot(ctx, work.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("working approval: %+v %v", snapshot, err)
	}
	if _, err := a.Work.Approve(asPerson("test"), workflow.Approval{StepID: snapshot.Steps[0].ID, Decision: workflow.VoteApprove}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Advance(asPerson("test"), work.ID); err != nil {
		t.Fatal(err)
	}

	// Landing still working (pending gate) → blocks.
	startLanding(workflow.StatusPending)
	if _, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "work"}); err == nil ||
		!strings.Contains(err.Error(), "already has a run in flight") {
		t.Fatalf("expected in-flight block, got %v", err)
	}
}

// The tool's boot verdict must be available in the next agent's feedback.
func TestBootFailureBecomesAgentFeedback(t *testing.T) {
	t.Parallel()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "accept", Uses: "approval"}}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi", "preview"}
		runner := func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			outcome := workflow.StepOutcome{Status: workflow.StatusPass}
			if task.Step().Type == "preview" {
				outcome = workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"app did not come up in a cold container","logTail":"mise ERROR No version is set for shim: go"}`), Rework: true}
			}
			if err := task.Finish(outcome, nil); err != nil {
				t.Fatal(err)
			}
		}
		deps.Runners = map[string]workflow.Runner{"agent/pi": runner, "preview": runner}
	})
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type != "approval" {
			if err := a.Work.ExecuteStep(ctx, step.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	fb := workflow.Interpolate(ctx, a.DB, "${{feedback.latest}}", run, nil, "p")
	if !strings.Contains(fb, "cold boot gate") || !strings.Contains(fb, "No version is set for shim: go") {
		t.Fatalf("preview crash not rendered as feedback: %q", fb)
	}
}

// A review that was not run because the boot failed settles after the boot,
// so it is the newest failure; the agent's next prompt must still carry the
// boot's own log line, not only "the app did not come up".
func TestABootFailuresLogReachesTheReworkPromptPastTheUnrunReview(t *testing.T) {
	t.Parallel()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "review", Uses: "agent/review"}, {Name: "accept", Uses: "approval"}}}}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi", "preview", "agent/review"}
		runner := func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			outcome := workflow.StepOutcome{Status: workflow.StatusPass}
			if task.Step().Type == "preview" {
				outcome = workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"app did not come up in a cold container","logTail":"PG::UndefinedTable: relation \"books\" does not exist"}`), Rework: true}
			}
			if err := task.Finish(outcome, nil); err != nil {
				t.Fatal(err)
			}
		}
		deps.Runners = map[string]workflow.Runner{"agent/pi": runner, "preview": runner, "agent/review": a.agents.Review}
	})
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type != "approval" {
			if err := a.Work.ExecuteStep(ctx, step.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range after.Steps {
		if step.Type == "agent/review" && (step.Status != workflow.StatusFail || !strings.Contains(string(step.Evidence), "notRun")) {
			t.Fatalf("the review must be recorded as not run on a failed boot: %s %s", step.Status, step.Evidence)
		}
	}
	fb := workflow.Interpolate(ctx, a.DB, "${{feedback.latest}}", run, nil, "p")
	if !strings.Contains(fb, `relation "books" does not exist`) {
		t.Fatalf("the boot's log line must reach the rework prompt: %q", fb)
	}
}

// TestLandingSerialization: one landing at a time per project — a second
// change's landing is refused while another is in flight (its gates would
// otherwise validate against a main that no longer exists at merge time).
func TestLandingSerialization(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	landYAML := `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: land, description: d}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: gates
      steps:
        - {name: ok, uses: approval, with: {approvers: [x], means: "the landing is ready"}}
`
	seedLibraryWorkflow(t, a, p.ID, "land", landYAML)
	ctx := a.asForge(context.Background(), p.ID, "test")
	first, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(a.asForge(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: first.ID, Workflow: "land"}); err != nil {
		t.Fatalf("first landing refused: %v", err)
	}
	// The refusal carries its code: a client waiting its turn reads that,
	// never the sentence.
	if _, err := a.Work.StartRun(a.asForge(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: second.ID, Workflow: "land"}); err == nil ||
		fault.CodeOf(err) != "landing-occupied" || fault.KindOf(err) != fault.Conflict {
		t.Fatalf("second landing not serialized: %v", err)
	}
}
