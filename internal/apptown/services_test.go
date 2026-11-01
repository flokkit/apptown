package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestSupersedeWedgedLanding(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)

	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "test", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "test", "fixture")
	landYAML := `apiVersion: apptown.dev/v1
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
	workYAML := `apiVersion: apptown.dev/v1
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
	ctx := a.asAppTown(context.Background(), p.ID, "test")
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
	landYAML := `apiVersion: apptown.dev/v1
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
	ctx := a.asAppTown(context.Background(), p.ID, "test")
	first, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(a.asAppTown(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: first.ID, Workflow: "land"}); err != nil {
		t.Fatalf("first landing refused: %v", err)
	}
	// The refusal carries its code: a client waiting its turn reads that,
	// never the sentence.
	if _, err := a.Work.StartRun(a.asAppTown(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: second.ID, Workflow: "land"}); err == nil ||
		fault.CodeOf(err) != "landing-occupied" || fault.KindOf(err) != fault.Conflict {
		t.Fatalf("second landing not serialized: %v", err)
	}
}

// TestMergedLandingDoesNotBlockFixForward: a landing whose merge is recorded
// but which cannot ship (its deploy refused) no longer holds the project's
// landing slot. A later change's landing is admitted beside it, names it,
// and on shipping settles it: its run is superseded and its change ships with
// the later one. A landing blocked before its merge still holds the slot.
func TestMergedLandingDoesNotBlockFixForward(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	landYAML := `apiVersion: apptown.dev/v1
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
	ctx := a.asAppTown(context.Background(), p.ID, "test")
	mk := func(title string) *workflow.Change {
		ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	testAdministrator(t, a, "x")
	stuck, blocker, fix := mk("stuck after merge"), mk("before merge"), mk("fix forward")
	stuckRun, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: stuck.ID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	// Before its merge is recorded the landing holds the slot.
	if _, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: blocker.ID, Workflow: "land"}); fault.CodeOf(err) != "landing-occupied" {
		t.Fatalf("a landing before its merge must hold the slot: %v", err)
	}
	// SCM records a merge only through a real Git merge of a published
	// branch, which this guarantee does not need; the row is the fact
	// HasActiveLanding reads (scm/schema.sql run_merges).
	// sql-guarantee: record the stuck landing's merge as SCM would.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO run_merges (run_id,state,operation,expected,result) VALUES ($1,'merged','merge','aaaa','bbbb')`, stuckRun.ID); err != nil {
		t.Fatal(err)
	}
	fixRun, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: fix.ID, Workflow: "land"})
	if err != nil {
		t.Fatalf("a merged landing that cannot ship must not block the fix-forward: %v", err)
	}
	snap, err := a.Work.Snapshot(ctx, fixRun.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var ok ids.ID
	for _, s := range snap.Steps {
		if s.Name == "ok" {
			ok = s.ID
		}
	}
	if _, err := a.Work.Approve(asPerson("x"), workflow.Approval{StepID: ok, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("x"), ok)}); err != nil {
		t.Fatal(err)
	}
	var shipped *workflow.Change
	eventually(t, "the stuck landing's change ships with the fix-forward", func() bool {
		ch, err := a.Work.Change(ctx, stuck.ID)
		if err != nil {
			t.Fatal(err)
		}
		shipped = ch
		return ch.State == workflow.ChangeShipped
	})
	if want := workflow.ClosedShippedWith; shipped.ClosedAs != want {
		t.Fatalf("superseded change closed as %q, want %q", shipped.ClosedAs, want)
	}
	old, err := a.Work.Snapshot(ctx, stuckRun.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if old.Run.Status != workflow.RunAbandoned {
		t.Fatalf("the superseded landing is %s, want abandoned", old.Run.Status)
	}
}
