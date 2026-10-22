package apptown

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestShutdownClosesAllOwnersBeforeWaiting(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "shutdown")
	change := createReadTestChange(t, a, pid, "shutdown")
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "shutdown", Version: 1}, Spec: schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "running", Uses: "script"}}, Steps: []schema.Step{{Name: "queued", Uses: "script"}}}}}})
	workflowEntered, workflowRelease := make(chan struct{}), make(chan struct{})
	var workflowOnce sync.Once
	releaseWorkflow := func() { workflowOnce.Do(func() { close(workflowRelease) }) }
	t.Cleanup(func() { releaseWorkflow(); _ = a.ShutdownBackground(context.Background()) })

	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			close(workflowEntered)
			<-workflowRelease
			// Its process was killed with AppTown: a failure seen now is
			// not recorded, and the step is left for startup repair.
			if err := task.Settle(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "exit 143"}, nil)); !errors.Is(err, workflow.ErrShuttingDown) {
				t.Errorf("a failure settled while AppTown stops: %v", err)
			}
		}}
	})
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: change, Workflow: "shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	runID := run.ID
	awaitRemediationSignal(t, workflowEntered)
	snapshot, err := a.Work.Snapshot(asPerson("bls"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var queuedID, runningID ids.ID
	for _, step := range snapshot.Steps {
		switch step.Name {
		case "queued":
			queuedID = step.ID
		case "running":
			runningID = step.ID
		}
	}
	if queuedID.IsZero() {
		t.Fatal("queued step missing")
	}
	plan := workflow.DispatchPlan{RunID: runID, Pending: 1, Steps: []workflow.ReadyStep{{ID: queuedID, Type: "script", Name: "queued"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.ShutdownBackground(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked owners should time out: %v", err)
	}
	if a.Work.Dispatch(plan) || !a.consoleStopped() {
		t.Fatal("one owner was left accepting work while shutdown waited for the other")
	}
	first := a.Work.BeginStop()
	if first != a.Work.BeginStop() {
		t.Fatal("repeated stops created independent waiters")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := a.ShutdownBackground(ctx2); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "workflow dispatch") {
		t.Fatalf("workflow runner's unfinished state was hidden: %v", err)
	}
	releaseWorkflow()
	awaitRemediationSignal(t, first)
	cancelled, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	if err := a.ShutdownBackground(cancelled); err != nil {
		t.Fatalf("completed owners should win over cancelled wait context: %v", err)
	}
	state, err := a.Work.Step(asPerson("bls"), queuedID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Step.Status != workflow.StatusPending {
		t.Fatal("shutdown advanced to queued workflow work")
	}
	if state, err = a.Work.Step(asPerson("bls"), runningID); err != nil || state.Step.Status != workflow.StatusRunning {
		t.Fatalf("the step that failed while AppTown stopped: %+v %v; want it still running, for startup repair", state, err)
	}
}

func TestBackgroundLoopsRespectCancelledContext(t *testing.T) {
	t.Parallel()
	// No DB: a pre-cancelled loop must not start a sweep or claim work.
	a := &App{}
	for name, run := range map[string]func(context.Context){
		"scheduler": a.RunScheduler,
		"health":    a.RunHealthController,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			done := make(chan struct{})
			go func() { defer close(done); run(ctx) }()
			awaitRemediationSignal(t, done)
		})
	}
}

// The App's background work runs under its lifetime, which shutdown ends:
// the console rounds, remediation's caller and the engine's
// requests stop with the App, not with whatever request started them.
//
// Kills: a shutdown that leaves the lifetime running.
func TestShutdownEndsTheAppsLifetime(t *testing.T) {
	t.Parallel()
	a := constructApp(t, testDB(t), Config{DataDir: t.TempDir()})
	if a.life.Err() != nil {
		t.Fatal("a new App's lifetime has already ended")
	}
	if err := a.ShutdownBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.life.Err() == nil {
		t.Fatal("shutdown left the App's lifetime running")
	}
}
