package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/ids"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// A completion whose run receipt, change move, release, release change or
// cleanup job fails leaves the run running, the change as it was, and no
// release or job. A failed completion changes nothing, so the faults are
// tried in turn on one run; the completion then succeeds and records its
// release.
func TestWorkflowCompletionRollsBackEveryRequiredRecord(t *testing.T) {
	t.Parallel()
	a, ctx, run, _ := completionReleaseScenario(t)
	store := a.DB
	before, err := a.Work.Change(ctx, run.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"INSERT INTO run_events", "UPDATE changes SET state=", "INSERT INTO releases", "INSERT INTO release_changes", "INSERT INTO jobs"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "INSERT INTO jobs" {
				failNextJobInsert(t, store)
			} else {
				dbtest.FailNextStatement(t, store, failure)
			}
			if _, err := a.Work.AdvanceFrom(ctx, run); err == nil {
				t.Fatalf("completion error: %v", err)
			}
			requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == run.ID && r.Status == workflow.RunRunning })
			after, err := a.Work.Change(ctx, run.ChangeID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("change escaped rollback: before=%+v after=%+v err=%v", before, after, err)
			}
			requireEvents(t, a, 0, eventKind("run.done"))
			for _, table := range []string{"releases", "release_changes", "river_job"} {
				// sql-guarantee: rollback must leave no hidden row in any of these tables, including rows no owner read would list.
				completionCount(t, store, 0, `SELECT COUNT(*) FROM `+table)
			}
		})
	}
	if out, err := a.Work.AdvanceFrom(ctx, run); err != nil || !out.Advanced {
		t.Fatal(err)
	}
	requireReleases(t, a, run.ProjectID, 1, func(r *deployment.Release) bool { return r.RunID == run.ID })
}

func TestWorkflowCompletionConcurrentWinner(t *testing.T) {
	t.Parallel()
	a, ctx, run, bundle := completionReleaseScenario(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := a.Work.AdvanceFrom(ctx, run)
			if err == nil && !out.Advanced {
				err = workflow.ErrRunMoved
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, workflow.ErrRunMoved) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d completion winners", winners)
	}
	requireReleases(t, a, run.ProjectID, 1, func(r *deployment.Release) bool { return r.RunID == run.ID && r.BundleID == bundle })
	requireChangeReleases(t, a, run.ProjectID, run.ChangeID, 1)
	requireEvents(t, a, 1, runEventKind(run.ID, "run.done"))
	requireJobs(t, a, 2, func(_ *rivertype.JobRow, r jobs.Request) bool { return r.Operation == workflowCompletionJob })
}

func TestWorkflowCompletionExplicitRetryOnlyRequeues(t *testing.T) {
	t.Parallel()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}}})
	s := a.DB
	if out, err := a.Work.Advance(ctx, run.ID); err != nil || !out.Advanced {
		t.Fatal(err)
	}
	pending, err := a.Jobs().Client.JobList(context.Background(), river.NewJobListParams())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := 0
	for _, row := range pending.Jobs {
		var request jobs.Request
		if err := json.Unmarshal(row.EncodedArgs, &request); err != nil {
			t.Fatal(err)
		}
		if request.Key != fmt.Sprintf("%d/land", run.ID) {
			continue
		}
		finished := time.Now()
		if _, err := riverdatabasesql.New(s.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: row.ID, StateDoUpdate: true, State: rivertype.JobStateCancelled, FinalizedAtDoUpdate: true, FinalizedAt: &finished}); err != nil {
			t.Fatal(err)
		}
		cancelled++
	}
	if cancelled != 1 {
		t.Fatalf("cancel landing follow-up: selected %d", cancelled)
	}
	restarted := restartApp(t, s)
	result, err := restarted.Work.Advance(ctx, run.ID)
	if err != nil || result.Advanced || result.RunStatus != workflow.RunDone || !result.FollowUpsPending {
		t.Fatalf("retry: %+v %v", result, err)
	}
	requireJobs(t, a, 3, func(j *rivertype.JobRow, _ jobs.Request) bool { return j.State == rivertype.JobStateAvailable })
	requireEvents(t, a, 1, eventKind("run.done"))
}

func TestWorkflowCompletionReturnsBeforeFollowUps(t *testing.T) {
	t.Parallel()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}}})
	withTestJobKind(t, a, workflowCompletionJob, 3, func(context.Context, string, map[string]any) (map[string]any, error) {
		t.Error("request executed a queued follow-up")
		return nil, nil
	})
	for _, first := range []bool{true, false} {
		res, err := a.Work.Advance(ctx, run.ID)
		if err != nil || res.Advanced != first || res.RunStatus != workflow.RunDone || !res.FollowUpsPending {
			t.Fatalf("advance: %+v %v", res, err)
		}
		requireJobs(t, a, 3, func(j *rivertype.JobRow, _ jobs.Request) bool {
			return j.State == rivertype.JobStateAvailable && j.Attempt == 0
		})
	}
	requireEvents(t, a, 1, eventKind("run.done"))
}

func changeScenario(t *testing.T, spec schema.Spec, additional ...workflow.PreparedRun) (*App, context.Context, *workflow.Change) {
	t.Helper()
	return changeScenarioIn(t, testApp(t), spec, additional...)
}

// changeScenarioIn admits the scenario into an already constructed application.
func changeScenarioIn(t *testing.T, a *App, spec schema.Spec, additional ...workflow.PreparedRun) (*App, context.Context, *workflow.Change) {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "completion", Name: "Completion", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "alice", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(p.ID), "fixture", "alice", "")
	ctx := asPerson("alice")
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Git = fakeGit{}
		// A fake deploy runner reports through the same claim/outcome commands
		// as a real runner. Calls and approvals are driven explicitly by each test.
		deps.Automatic = []string{"deploy"}
		deps.Report = func(err error) { t.Error(err) }
		deps.Runners = map[string]workflow.Runner{"deploy": func(task *workflow.Execution) {
			_, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}}

	})
	defineWorkflows(t, a, append([]workflow.PreparedRun{{Workflow: workflow.DefinitionRef{Name: "test", Version: 1}, Spec: spec}}, additional...)...)
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "A change"})
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, ch
}

func completionScenario(t *testing.T, spec schema.Spec, additional ...workflow.PreparedRun) (*App, context.Context, *workflow.Run) {
	t.Helper()
	a, ctx, ch := changeScenario(t, spec, additional...)
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, snapshot.Run
}

// This is the composition guarantee left after stale-child cases moved to Work:
// Forge hands the durable key to Work; Work validates and advances its parent.
func TestWorkflowCompletionParentJobAdvancesOnlyOnce(t *testing.T) {
	t.Parallel()
	a, ctx, parent := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "finish", Steps: []schema.Step{{Name: "child", Uses: "workflow/call"}}},
		{Name: "later", Steps: []schema.Step{{Name: "review", Uses: "approval"}}},
	}}, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish"}}}})
	// This test drives child completion and parent-job delivery explicitly.
	// Do not race the empty child's automatic dispatcher.
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, parent.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("parent: %+v %v", snapshot, err)
	}
	child, err := a.Work.Call(ctx, workflow.WorkflowCall{ParentStepID: snapshot.Steps[0].ID, Workflow: "child"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if out, err := a.Work.Advance(ctx, child.ID); err != nil || !out.Advanced {
		t.Fatalf("child: %+v %v", out, err)
	}
	queued, err := jobs.List(ctx, a.DB, 50)
	if err != nil {
		t.Fatal(err)
	}
	key, found := fmt.Sprintf("%d/parent", child.ID), 0
	for _, job := range queued {
		if job.Kind == workflowCompletionJob && job.Key == key {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("parent completion queued %d tasks: %+v", found, queued)
	}
	dbtest.FailNextStatement(t, a.DB, "UPDATE runs SET current_stage")
	if _, err := workflowFollowUps(a).Complete(context.Background(), key, nil); err == nil {
		t.Fatal("parent settlement failure was hidden")
	}
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == parent.ID && r.CurrentStage == "finish" })
	requireEvents(t, a, 0, runEventKind(parent.ID, "advance.ok"))
	for range 2 {
		if _, err := workflowFollowUps(a).Complete(context.Background(), key, nil); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = a.Work.Snapshot(ctx, parent.ID, workflow.TraceView)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.CurrentStage != "later" || snapshot.Run.Status != workflow.RunRunning {
		t.Fatalf("parent moved beyond handoff: %+v", snapshot.Run)
	}
	advances := 0
	for _, event := range snapshot.Events {
		if event.Kind == "advance.ok" {
			advances++
		}
	}
	if advances != 1 {
		t.Fatalf("parent advanced %d times", advances)
	}
}

// A real declared deployment step settles through a fake external runner; an
// approval keeps the run open until the test explicitly requests completion.
func completionReleaseScenario(t *testing.T) (*App, context.Context, *workflow.Run, ids.ID) {
	t.Helper()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "deploy", Uses: "deploy", With: map[string]any{"deployment": "main"}},
		{Name: "accept", Uses: "approval"},
	}}}})
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture prepares a settlement for explicit failure injection.
	// Stop scheduling before the approval so completion remains the test action.
	a.Work.Stop()
	deployed, approved := false, false
	for _, step := range snapshot.Steps {
		if step.Name == "deploy" {
			deployed = step.Status == workflow.StatusPass
		}
		if step.Name == "accept" {
			result, err := a.Work.Approve(ctx, workflow.Approval{StepID: step.ID, Decision: workflow.VoteApprove})
			if err != nil {
				t.Fatal(err)
			}
			approved = result.Status == workflow.StatusPass
		}
	}
	if !deployed || !approved {
		t.Fatalf("unfinished deployment/approval: %+v", snapshot.Steps)
	}
	bundle, err := a.Deployment.Bundles.Cut(ctx, run.ProjectID, "tree", map[string]string{"web": "image"}, nil, []byte("contract"), deployment.BundleOrigin{RunID: run.ID, ChangeID: run.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, snapshot.Run, bundle
}
