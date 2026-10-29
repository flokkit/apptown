package apptown

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// startAdoptionForTest reads the receipt a start resolves, through the
// admission source Preparation uses.
func startAdoptionForTest(ctx context.Context, a *App, req workflow.StartRequest) (*adoption.RecordedRevision, error) {
	source, err := a.Work.StartSource(a.asAppTown(ctx, req.ProjectID, "test"), req)
	if err != nil {
		return nil, err
	}
	return adoption.ProjectAdoptionIn(ctx, a.DB, req.ProjectID, source.AdoptionID)
}

// defineWorkflows replaces only external definition lookup. Admission,
// workspace preparation, attribution and dispatch still belong to Work.
func defineWorkflows(t *testing.T, a *App, definitions ...workflow.PreparedRun) {
	t.Helper()
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Resolve = workcommands.NewPreparation(a.DB, definedWorkflows{Service: a.Adoption, definitions: definitions}, a.branches, a.cfg.dataDir, holderPresenceFor(a), a.chatSaves, a.Secrets, nil).Resolve
	})
}

type definedWorkflows struct {
	*adoption.Service
	definitions []workflow.PreparedRun
}

func (d definedWorkflows) Workflow(ctx context.Context, req adoption.WorkflowRequest) (*adoption.ResolvedWorkflow, error) {
	for _, definition := range d.definitions {
		if definition.Workflow.Name == req.Name {
			return &adoption.ResolvedWorkflow{Name: req.Name, Version: definition.Workflow.Version, Origin: definition.Workflow.Origin, Doc: &schema.Doc{Spec: definition.Spec}}, nil
		}
	}
	return nil, fmt.Errorf("test has no definition %q", req.Name)
}

// These are AppTown composition tests. Work's own transaction tests live in
// workflow/run_admission_transactions_test.go; no run or step is seeded here.
func TestRunStartRecoveryPreparesBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"ready", "failed workspace", "approval only"} {
		t.Run(scenario, func(t *testing.T) {
			stage := schema.Stage{Name: "first", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}
			automatic := scenario != "approval only"
			if automatic {
				stage.OnEnter = []schema.Step{{Name: "check", Uses: "script"}}
			}
			a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{stage}})
			if err := a.Projects.SetDefaultBranch(ctx, change.ProjectID, "main"); err != nil {
				t.Fatal(err)
			}
			unavailable := errors.New("disk unavailable")
			rebuildWork(t, a, func(deps *workflow.Dependencies) {
				deps.Automatic = []string{"script"}
				deps.Runners = map[string]workflow.Runner{"script": func(*workflow.Execution) { t.Error("original process dispatched work") }}
				if scenario != "ready" {
					deps.Git = fakeGit{clone: func(workflow.CloneRequest) (workflow.WorkspaceResult, error) {
						return workflow.WorkspaceResult{}, unavailable
					}}
				}
			})
			a.Work.Stop()
			run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
			if run == nil || (scenario == "ready" && !errors.Is(err, workflow.ErrDispatchStopped)) || (scenario != "ready" && !errors.Is(err, unavailable)) {
				t.Fatalf("start: %+v %v", run, err)
			}
			if scenario != "ready" {
				// sql-guarantee: readiness publication is the run_starts row itself; Work exposes no read of it, and the guarantee is that this exact row stays unready until recovery.
				completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM run_starts WHERE run_id=$1 AND NOT ready`, run.ID)
				requireEvents(t, a, 1, runEventKind(run.ID, "branch.error"))
			}
			// This new composition has no original request or in-memory definition.
			restarted := restartApp(t, a.DB)
			var runnerCalls atomic.Int32
			rebuildWork(t, restarted, func(deps *workflow.Dependencies) {
				deps.Git = fakeGit{}
				deps.Automatic = []string{"script"}
				deps.Report = func(err error) { t.Error(err) }
				deps.Resolve = func(context.Context, workflow.StartRequest, workflow.StartSource) (workflow.PreparedRun, error) {
					t.Error("recovery re-resolved a recorded run")
					return workflow.PreparedRun{}, errors.New("unexpected resolution")
				}
				deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
					var ready bool
					// sql-guarantee: readiness publication is the run_starts row itself; Work exposes no read of it, and the guarantee is that this exact row stays unready until recovery.
					if err := a.DB.SQL().QueryRowContext(context.Background(), `SELECT ready FROM run_starts WHERE run_id=$1`, run.ID).Scan(&ready); err != nil || !ready {
						t.Errorf("runner started before readiness: %t %v", ready, err)
						return
					}
					_, err := task.Claim()
					if err != nil {
						t.Error(err)
						return
					}
					runnerCalls.Add(1)
					if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
						t.Error(err)
					}
				}}
			})
			for range 2 {
				restarted.ResumePendingAutoSteps(ctx)
				restarted.Work.Wait()
			}
			wantCalls := int32(0)
			if automatic {
				wantCalls = 1
			}
			if runnerCalls.Load() != wantCalls {
				t.Fatalf("runner calls: %d, want %d", runnerCalls.Load(), wantCalls)
			}
			snapshot, err := restarted.Work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil || snapshot.Run.Status != workflow.RunRunning {
				t.Fatalf("recovered run: %+v %v", snapshot, err)
			}
			for _, step := range snapshot.Steps {
				want := workflow.StatusPending
				if step.Type == "script" {
					want = workflow.StatusPass
				}
				if step.Status != want {
					t.Fatalf("step %s: %s, want %s", step.Name, step.Status, want)
				}
			}
			requireEvents(t, a, 1, runEventKind(run.ID, "run.ready"))
			requireRuns(t, a, 1, nil)
		})
	}
}

func TestRunStartRecoveryPreservesChildInvocation(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint("empty=", empty), func(t *testing.T) {
			childStage := schema.Stage{Name: "first"}
			if !empty {
				childStage.Steps = []schema.Step{{Name: "accept", Uses: "approval"}}
			}
			a, ctx, parent := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "accept", Uses: "approval"}, {Name: "call", Uses: "workflow/call"}}}}},
				workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{childStage}}})
			snapshot, err := a.Work.Snapshot(ctx, parent.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var callID ids.ID
			for _, step := range snapshot.Steps {
				if step.Type == "workflow/call" {
					callID = step.ID
				}
			}
			// Admission commits, but the old dispatcher no longer accepts work.
			a.Work.Stop()
			if !empty {
				// Lose readiness publication after admission committed. Startup
				// must retain this live link while preparing its child again.
				dbtest.FailNextStatement(t, a.DB, "UPDATE run_starts SET ready")
			}
			child, err := a.Work.Call(ctx, workflow.WorkflowCall{ParentStepID: callID, Workflow: "child"})
			if child == nil || (empty && !errors.Is(err, workflow.ErrDispatchStopped)) || (!empty && err == nil) {
				t.Fatalf("child admission: %+v %v", child, err)
			}
			if !empty {
				// sql-guarantee: readiness publication is the run_starts row itself; Work exposes no read of it, and the guarantee is that this exact row stays unready until recovery.
				completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM run_starts WHERE run_id=$1 AND NOT ready`, child.ID)
			}
			restarted := restartApp(t, a.DB)
			rebuildWork(t, restarted, func(deps *workflow.Dependencies) {
				deps.Git = fakeGit{}
				deps.Report = func(err error) { t.Error(err) }
			})
			for range 2 {
				restarted.ResumePendingAutoSteps(ctx)
				restarted.Work.Wait()
			}
			childSnapshot, err := restarted.Work.Snapshot(ctx, child.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			call, err := restarted.Work.Step(ctx, callID)
			if err != nil {
				t.Fatal(err)
			}
			wantRun, wantStep := workflow.RunRunning, workflow.StatusRunning
			if empty {
				wantRun, wantStep = workflow.RunDone, workflow.StatusPass
			}
			if childSnapshot.Run.Status != wantRun || call.Step.Status != wantStep || childSnapshot.Run.ParentStepID != callID {
				t.Fatalf("recovered invocation: child=%+v call=%+v", childSnapshot.Run, call.Step)
			}
			requireSteps(t, a, 1, func(s *workflow.Step) bool {
				return s.ID == callID && evidenceField(s, "childRunId") == fmt.Sprint(child.ID)
			})
			requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == parent.ID && r.Status == workflow.RunRunning })
			requireRuns(t, a, 2, nil)
		})
	}
}

func TestRunStartChildCallCannotOverwriteFastCompletion(t *testing.T) {
	t.Parallel()
	a, ctx, parent := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "child"}}, {Name: "accept", Uses: "approval"}}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "first"}}}})
	var resolves atomic.Int32
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners["workflow/call"] = workflowexec.NewCalls(a.DB, jobReporter{}).Run
		resolve := deps.Resolve
		deps.Resolve = func(ctx context.Context, req workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			resolves.Add(1)
			return resolve(ctx, req, source)
		}
	})
	snapshot, err := a.Work.Snapshot(ctx, parent.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var callID ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "workflow/call" {
			callID = step.ID
		}
	}
	for range 2 {
		a.Work.ExecuteStep(a.asEngine(context.Background()), callID)
		a.Work.Wait()
	}
	if resolves.Load() != 1 {
		t.Fatalf("resolved a recorded invocation %d times", resolves.Load())
	}
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.WorkflowName == "child" && r.Status == workflow.RunDone })
	requireSteps(t, a, 1, func(s *workflow.Step) bool {
		return s.ID == callID && s.Status == workflow.StatusPass && evidenceField(s, "outcome") == "done"
	})
	requireEvents(t, a, 1, runEventKind(parent.ID, "run.called"))
}

func TestRunStartReceiptLookupFailureLeavesNoRun(t *testing.T) {
	t.Parallel()
	a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	dbtest.FailNextStatement(t, a.DB, "-- name: Bindings :many")
	if run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"}); err == nil || run != nil {
		t.Fatalf("receipt lookup failure: %+v %v", run, err)
	}
	// Every event belongs to a run (a foreign key), so no run means no event.
	requireRuns(t, a, 0, nil)
	requireEvents(t, a, 0, nil)
}
