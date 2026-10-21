package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	models "flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/poll/polltest"
)

// A second script is not admitted while another script task of the run is
// unsettled, whatever state the other's step is in, even one still at work:
// that task's stop could fail while the second waits to start. It is
// refused before its own task is created, and the other task is left as it
// was.
func TestASecondScriptIsNotAdmittedBesideAnUnsettledTask(t *testing.T) {
	t.Parallel()
	a, fake, firstStep := scriptTaskFixture(t,
		map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
		schema.Step{Name: "second", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_EXIT": "0"}}})
	ctx := context.Background()
	first := taskKeyOf(firstStep)
	t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: first}) })
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.Work.ExecuteStep(a.asEngine(ctx), firstStep.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	before, err := a.tasks.Records.Get(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if running, err := workflow.ReadStepIn(ctx, a.DB, firstStep.ID); err != nil || running.Status != workflow.StatusRunning {
		t.Fatalf("fixture: the first step is not at work: %+v %v", running, err)
	}
	snap, err := a.Work.Snapshot(a.asEngine(ctx), firstStep.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name != "second" {
			continue
		}
		if err := a.Work.ExecuteStep(a.asEngine(ctx), s.ID); err != nil {
			t.Fatal(err)
		}
		got, err := workflow.ReadStepIn(ctx, a.DB, s.ID)
		if err != nil || got.Status != workflow.StatusError {
			t.Fatalf("a second script was admitted beside an unsettled task: %+v %v", got, err)
		}
		if fake.Present(taskKeyOf(s)) || fake.Starts(taskKeyOf(s)) != 0 {
			t.Fatal("the second script's task was created or started")
		}
	}
	after, err := a.tasks.Records.Get(ctx, first)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("the refusal changed the unsettled task: before=%+v after=%+v err=%v", before, after, err)
	}
}

// Every read the unstopped-task check makes fails closed: the attempt is
// refused, and the running task's record is left as it was.
func TestTheUnstoppedTaskCheckRefusesOnAnyReadError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query string
		skip  int
	}{
		{"UnsettledAttempts", 0}, {"RunRoot", 0}, {"RunTreeActivity", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			a, fake, firstStep := scriptTaskFixture(t,
				map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
				schema.Step{Name: "second", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_EXIT": "0"}}})
			ctx := context.Background()
			first := taskKeyOf(firstStep)
			t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: first}) })
			a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
			if err := a.Work.ExecuteStep(a.asEngine(ctx), firstStep.ID); err != nil {
				t.Fatal(err)
			}
			before, err := a.tasks.Records.Get(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := a.Work.Snapshot(a.asEngine(ctx), firstStep.RunID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var secondStep *workflow.Step
			for _, s := range snap.Steps {
				if s.Name == "second" {
					secondStep = s
				}
			}
			if secondStep == nil {
				t.Fatal("second step missing")
			}
			a.tasks.AbandonAt = nil
			restore := a.DB.FailStatementAfter("-- name: "+tc.query+" ", tc.skip)
			defer restore()
			if err := a.Work.ExecuteStep(a.asEngine(ctx), secondStep.ID); err != nil {
				t.Fatal(err)
			}
			restore()
			got, err := workflow.ReadStepIn(ctx, a.DB, secondStep.ID)
			if err != nil || got.Status != workflow.StatusError {
				t.Fatalf("read failure did not refuse the writer: %+v %v", got, err)
			}
			if fake.Present(taskKeyOf(secondStep)) || fake.Starts(taskKeyOf(secondStep)) != 0 {
				t.Fatal("read failure started another writer")
			}
			after, err := a.tasks.Records.Get(ctx, first)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("fence changed the existing task: before=%+v after=%+v err=%v", before, after, err)
			}
			obs, err := fake.Observe(ctx, execution.TaskRef{Key: first})
			if err != nil || obs.State != execution.TaskRunning {
				t.Fatalf("fence touched running task: %+v %v", obs, err)
			}
		})
	}
}

// Steps that write one folder do not overlap.

// Resolving the CLI is a real pre-start boundary in piInvoke, after the
// agent's unsettled-task check. The callback orders an overlapping owner
// operation there, without changing any workflow or task state directly.
type pausingCLI struct{ before func() }

func (c *pausingCLI) Path() (string, error) {
	if c.before != nil {
		f := c.before
		c.before = nil
		f()
	}
	return "", fmt.Errorf("no CLI needed by the fake model process")
}

// An agent and a script asked for at once in one change folder do not
// overlap: a script asked for while the agent works waits until the agent
// has finished (the folder lock), and an agent asked for after a script whose
// task is unsettled (running, or its stop failed) is refused. Both owner
// calls are Work.ExecuteStep; normal dispatch is stopped by the fixture.
func TestAnAgentAndAScriptNeverShareTheFolder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		executor    string
		failedStop  bool
		scriptFirst bool
	}{{ExecutorLocal, false, false}, {ExecutorDocker, false, false}, {ExecutorLocal, true, true}, {ExecutorDocker, true, true}} {
		failedStop := tc.failedStop
		t.Run(fmt.Sprintf("%s/failed-stop=%v/script-first=%v", tc.executor, failedStop, tc.scriptFirst), func(t *testing.T) {
			a, fake, script := scriptTaskFixture(t,
				map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
				schema.Step{Name: "agent", Uses: "agent/pi", With: map[string]any{"prompt": "do it", "executor": tc.executor}})
			ctx := context.Background()
			a.cfg.agentExecutor = tc.executor
			containers := newSurvivingContainers()
			containers.allowRemoval()
			a.docker = docker.Scope{Client: containers}
			snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var agent *workflow.Step
			for _, s := range snap.Steps {
				if s.Name == "agent" {
					agent = s
				}
			}
			if agent == nil {
				t.Fatal("missing agent step")
			}
			key := taskKeyOf(script)
			driver := &unstoppableTasks{TaskDriver: fake, workspaces: map[string]string{}}
			a.tasks.Driver = driver
			t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
			var before tasks.Attempt
			calls := 0
			startScript := func() {
				a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
				if err := a.Work.ExecuteStep(a.asEngine(ctx), script.ID); err != nil {
					t.Fatal(err)
				}
				a.tasks.AbandonAt = nil
				if failedStop {
					// Timeout, failed stop, step failure and task retention use production recovery.
					// sql-guarantee: the deadline passes during the outage, without waiting out the ten-minute budget.
					if _, err := a.DB.Exec(`UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=?`, key); err != nil {
						t.Fatal(err)
					}
					e, err := a.Work.RecoverExecution(a.asEngine(ctx), script.ID, 1)
					if err != nil {
						t.Fatal(err)
					}
					a.workDependencies().Recoverers["script"](e)
					s, err := workflow.ReadStepIn(ctx, a.DB, script.ID)
					if err != nil || s.Status != workflow.StatusError {
						t.Fatalf("script step: %+v %v", s, err)
					}
				}
				before, err = a.tasks.Records.Get(ctx, key)
				if err != nil || before.Observed == tasks.Settled || (failedStop && !before.StopWanted) {
					t.Fatalf("script task: %+v %v", before, err)
				}
			}
			// While the agent is between its checks and its model call, the
			// script is asked for from another caller; it must wait.
			scriptDone := make(chan struct{})
			cli := &pausingCLI{before: func() {
				go func() { defer close(scriptDone); startScript() }()
				select {
				case <-scriptDone:
				case <-time.After(500 * time.Millisecond):
				}
			}}
			a.cli = cli
			if tc.scriptFirst {
				startScript()
				cli.before = nil
				close(scriptDone)
			}
			overlapped := false
			installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
				calls++
				if fake.Present(key) {
					overlapped = true
				}
				return ExecOutcome{Output: piSays("done")}
			})
			if err := a.Work.ExecuteStep(a.asEngine(ctx), agent.ID); err != nil {
				t.Fatal(err)
			}
			if tc.scriptFirst {
				after, err := a.tasks.Records.Get(ctx, key)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("the refused agent disturbed the script's task: before=%+v after=%+v err=%v", before, after, err)
				}
				current, err := workflow.ReadStepIn(ctx, a.DB, agent.ID)
				if err != nil || current.Status != workflow.StatusError || calls != 0 {
					t.Fatalf("an agent was not refused beside an unsettled script task: %+v calls=%d %v", current, calls, err)
				}
				return
			}
			<-scriptDone
			if calls != 1 || overlapped {
				t.Fatalf("the agent ran beside the script's task (calls=%d, overlapped=%v)", calls, overlapped)
			}
			if !fake.Present(key) {
				t.Fatal("the script did not run once the agent had finished")
			}
		})
	}
}

// An agent or reviewer whose container outlived its step (a failed timeout
// cleanup, or AppTown stopping mid-step) keeps the next writer out of the
// folder, a script as much as an agent. All step claims and outcomes use
// Work; the crash case uses real startup repair; no workflow state or task
// record is written by the test. The script is refused before its task is
// recorded, so recovery has nothing to start here; a task recorded before a
// writer appeared is TestRecoveryDoesNotStartAScriptBesideAWriterOrAfterItsRunStopped's.
func TestAWriterThatOutlivedItsStepKeepsTheNextOneOut(t *testing.T) {
	t.Parallel()
	for _, oldKind := range []string{"agent/pi", "agent/review"} {
		for _, interrupted := range []bool{false, true} {
			for _, incoming := range []string{"script", "agent/pi"} {
				t.Run(fmt.Sprintf("%s/interrupted=%v/incoming=%s", oldKind, interrupted, incoming), func(t *testing.T) {
					a, fake, script := scriptTaskFixture(t,
						map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
						schema.Step{Name: "old", Uses: oldKind, With: map[string]any{"prompt": "old writer", "executor": "docker"}},
						schema.Step{Name: "next", Uses: "agent/pi", With: map[string]any{"prompt": "new writer", "executor": "docker"}})
					ctx := context.Background()
					a.cfg.agentExecutor = ExecutorDocker
					daemon := newSurvivingContainers() // removals fail: the old process is live
					a.docker = docker.Scope{Client: daemon}
					snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
					if err != nil {
						t.Fatal(err)
					}
					var old, next *workflow.Step
					for _, s := range snap.Steps {
						switch s.Name {
						case "old":
							old = s
						case "next":
							next = s
						}
					}
					if old == nil || next == nil {
						t.Fatal("missing fixture steps")
					}
					source, err := workflow.ReadSourceIn(ctx, a.DB, workflow.RunRef{ID: script.RunID, ProjectID: snap.Run.ProjectID})
					if err != nil {
						t.Fatal(err)
					}
					ws := source.Workspace(a.cfg.dataDir)
					var survived string
					calls := 0
					installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
						calls++
						if req.Workspace != ws {
							t.Fatalf("wrong agent workspace: %s", req.Workspace)
						}
						survived = req.Name
						daemon.survive(req)
						if interrupted {
							panic("test crash boundary: launched agent, no outcome recorded")
						}
						return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("Docker did not stop the agent")}
					})
					if err := a.Work.ExecuteStep(a.asEngine(ctx), old.ID); err != nil {
						t.Fatal(err)
					}
					before, err := workflow.ReadStepIn(ctx, a.DB, old.ID)
					if err != nil {
						t.Fatal(err)
					}
					want := workflow.StatusError
					if interrupted {
						want = workflow.StatusRunning
					}
					if before.Status != want || calls != 1 || !daemon.has(survived) {
						t.Fatalf("old writer not established: status=%s calls=%d live=%v", before.Status, calls, daemon.has(survived))
					}
					if interrupted {
						// The runner returned only to model process loss. Its lock has gone;
						// startup repair is required before the new process serves Work calls.
						if err := a.RecoverInterruptedWork(ctx); err != nil {
							t.Fatal(err)
						}
						after, err := workflow.ReadStepIn(ctx, a.DB, old.ID)
						if err != nil || after.Status != workflow.StatusError {
							t.Fatalf("startup repair: %+v %v", after, err)
						}
					}
					driver := &unstoppableTasks{TaskDriver: fake, workspaces: map[string]string{}}
					a.tasks.Driver = driver
					a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
					key := taskKeyOf(script)
					t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
					id := script.ID
					if incoming == "agent/pi" {
						id = next.ID
					}
					if err := a.Work.ExecuteStep(a.asEngine(ctx), id); err != nil {
						t.Fatal(err)
					}
					if !daemon.has(survived) {
						t.Fatal("fixture unexpectedly stopped its old writer")
					}
					if incoming == "agent/pi" {
						got, err := workflow.ReadStepIn(ctx, a.DB, next.ID)
						if err != nil || got.Status != workflow.StatusError || calls != 1 {
							t.Fatalf("Docker agent refusal control: %+v calls=%d %v", got, calls, err)
						}
						return
					}
					obs, err := fake.Observe(ctx, execution.TaskRef{Key: key})
					if err != nil {
						t.Fatal(err)
					}
					if obs.State == execution.TaskRunning && driver.workspaces[key] == ws {
						t.Fatalf("script started in %s beside surviving %s (interrupted=%v)", ws, oldKind, interrupted)
					}
					got, err := workflow.ReadStepIn(ctx, a.DB, script.ID)
					if err != nil || got.Status != workflow.StatusError {
						t.Fatalf("script must refuse until old writer stops: %+v %v", got, err)
					}
				})
			}
		}
	}
}

// A claim made before waiting for the new mutex is not permission to launch
// after an owner operation has abandoned that run during the wait.
func TestAStepThatWaitedForTheFolderDoesNotRunAfterItsRunStopped(t *testing.T) {
	t.Parallel()
	a, fake, script := scriptTaskFixture(t,
		map[string]any{"run": "true", "executor": "docker", "image": "fake"},
		schema.Step{Name: "agent", Uses: "agent/pi", With: map[string]any{"prompt": "hold the folder", "executor": "local"}})
	ctx := context.Background()
	snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	agent := snap.Steps[1]
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	installFakePi(t, a, func(_ int, _ ExecRequest) ExecOutcome {
		close(entered)
		<-release
		return ExecOutcome{Output: piSays("done")}
	})
	agentDone := make(chan error, 1)
	go func() { agentDone <- a.Work.ExecuteStep(a.asEngine(ctx), agent.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not enter")
	}
	scriptDone := make(chan error, 1)
	go func() { scriptDone <- a.Work.ExecuteStep(a.asEngine(ctx), script.ID) }()
	// The script waits for the folder before claiming its attempt, so while
	// the agent works it stays unclaimed and starts nothing.
	// Holds: the script waits on an in-process folder mutex, which records
	// nothing while it waits; the product has no "waiting for the folder"
	// state to witness.
	var claimed *workflow.Step
	var readErr error
	if !polltest.Holds(ctx, 300*time.Millisecond, func() bool {
		claimed, readErr = workflow.ReadStepIn(ctx, a.DB, script.ID)
		return readErr != nil || claimed.Status == workflow.StatusRunning || fake.Starts(taskKeyOf(script)) != 0
	}) {
		t.Fatalf("the script claimed its attempt or started while another step held the folder: %+v %v (starts %d)", claimed, readErr, fake.Starts(taskKeyOf(script)))
	}
	if _, err := a.Work.Abandon(a.asEngine(ctx), workflow.StopWork{RunID: script.RunID, Reason: "cancel while queued"}); err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(ctx, a.DB, script.RunID)
	if err != nil || run.Status == workflow.RunRunning {
		t.Fatalf("abandon did not commit: %+v %v", run, err)
	}
	close(release)
	released = true
	for _, done := range []chan error{agentDone, scriptDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("writer did not finish")
		}
	}
	if n := fake.Starts(taskKeyOf(script)); n != 0 {
		t.Fatalf("abandoned script started %d time(s) after acquiring the folder lock", n)
	}
}

// A script task recorded before a crash but never started is not started by
// recovery while something else still writes its folder (here a container
// left labelled with it), nor once its run has been stopped while it waited
// for the folder.
func TestRecoveryDoesNotStartAScriptBesideAWriterOrAfterItsRunStopped(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("run-stopped=%v", stopped), func(t *testing.T) {
			a, fake, script := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "image": "fake"})
			ctx := context.Background()
			daemon := newSurvivingContainers()
			a.docker = docker.Scope{Client: daemon}
			key := taskKeyOf(script)
			t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
			a.tasks.AbandonAt = func(stage string) bool { return stage == "created" }
			if err := a.Work.ExecuteStep(a.asEngine(ctx), script.ID); err != nil {
				t.Fatal(err)
			}
			if obs, err := fake.Observe(ctx, execution.TaskRef{Key: key}); err != nil || obs.State != execution.TaskCreated {
				t.Fatalf("fixture: the task is not created and unstarted: %+v %v", obs, err)
			}
			run, err := workflow.ReadRunIn(ctx, a.DB, script.RunID)
			if err != nil {
				t.Fatal(err)
			}
			source, err := workflow.ReadSourceIn(ctx, a.DB, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			e, err := a.Work.RecoverExecution(a.asEngine(ctx), script.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if stopped {
				if _, err := a.Work.Abandon(a.asEngine(ctx), workflow.StopWork{RunID: script.RunID, Reason: "stopped while recovering"}); err != nil {
					t.Fatal(err)
				}
			} else {
				daemon.live["left-writer"] = []string{docker.WritesLabel + "=" + source.Workspace(a.cfg.dataDir)}
			}
			a.tasks.AbandonAt = nil
			a.workDependencies().Recoverers["script"](e)
			if n := fake.Starts(key); n != 0 {
				t.Fatalf("recovery started the task %d time(s)", n)
			}
		})
	}
}

// A failed outcome write leaves a timed-out agent marked running after its
// runner releases the folder. That database status must not exempt its live
// container from exclusion. These are real owner calls and a one-shot database
// failure; no step or task record is manufactured.
func TestAWriterLeftMarkedRunningKeepsEveryNextWriterOut(t *testing.T) {
	t.Parallel()
	for _, oldKind := range []string{"agent/pi", "agent/review"} {
		for _, incoming := range []string{"script", "agent/pi", "agent/review", "host-agent", "host-review"} {
			t.Run(fmt.Sprintf("%s/to/%s", oldKind, incoming), func(t *testing.T) {
				kind, backend := incoming, ExecutorDocker
				switch incoming {
				case "script":
					kind = "agent/pi" // unused next step
				case "host-agent":
					kind, backend = "agent/pi", ExecutorLocal
				case "host-review":
					kind, backend = "agent/review", ExecutorLocal
				}
				a, fake, script := scriptTaskFixture(t,
					map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
					schema.Step{Name: "old", Uses: oldKind, With: map[string]any{"prompt": "old", "executor": "docker"}},
					schema.Step{Name: "next", Uses: kind, With: map[string]any{"prompt": "next", "executor": backend}})
				ctx := context.Background()
				a.cfg.agentExecutor = ExecutorDocker
				daemon := newSurvivingContainers()
				a.docker = docker.Scope{Client: daemon}
				snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				old, next := snap.Steps[1], snap.Steps[2]
				source, err := workflow.ReadSourceIn(ctx, a.DB, workflow.RunRef{ID: script.RunID, ProjectID: snap.Run.ProjectID})
				if err != nil {
					t.Fatal(err)
				}
				ws := source.Workspace(a.cfg.dataDir)
				var survivor string
				calls := 0
				runPi := func(_ int, req ExecRequest) ExecOutcome {
					calls++
					if req.Workspace != ws {
						t.Fatalf("wrong workspace: %s", req.Workspace)
					}
					if calls == 1 {
						survivor = req.Name
						daemon.survive(req)
						// The outcome and the error that would replace it both fail to
						// record: the step is left marked running, as a crash leaves it.
						a.agents.Settle = workflow.Settlement{Tries: 1}
						t.Cleanup(a.DB.FailStatements("-- name: FinishAttempt", 2))
						return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("agent remains alive")}
					}
					writeVerdict(t, ws, `{"verdict":"pass","summary":"checked","findings":[]}`)
					return ExecOutcome{Output: piSays("done")}
				}
				installFakePi(t, a, runPi)
				if err := a.Work.ExecuteStep(a.asEngine(ctx), old.ID); err != nil {
					t.Fatal(err)
				}
				before, err := workflow.ReadStepIn(ctx, a.DB, old.ID)
				if err != nil || before.Status != workflow.StatusRunning || calls != 1 || !daemon.has(survivor) {
					t.Fatalf("survivor fixture: step=%+v calls=%d live=%v err=%v", before, calls, daemon.has(survivor), err)
				}
				// Rebind the incoming review's host backend after the Docker
				// predecessor, retaining the same database and Docker scope.
				a.cfg.agentExecutor = backend
				installFakePi(t, a, runPi)
				key := taskKeyOf(script)
				t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
				if incoming == "script" {
					driver := &unstoppableTasks{TaskDriver: fake, workspaces: map[string]string{}}
					a.tasks.Driver = driver
					a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
					if err := a.Work.ExecuteStep(a.asEngine(ctx), script.ID); err != nil {
						t.Fatal(err)
					}
					if fake.Starts(key) != 0 && daemon.has(survivor) && driver.workspaces[key] == ws {
						t.Fatalf("%s started beside surviving %s whose failed outcome left it running", incoming, oldKind)
					}
				} else {
					if err := a.Work.ExecuteStep(a.asEngine(ctx), next.ID); err != nil {
						t.Fatal(err)
					}
					if calls != 1 && daemon.has(survivor) {
						t.Fatalf("%s invoked beside surviving %s whose failed outcome left it running", incoming, oldKind)
					}
				}
			})
		}
	}
}

// A remote task uses its own checkout, so an agent can be admitted after the
// remote runner was interrupted. Recovering that task still writes the local
// folder when its result is brought back, even though it starts no new task.
func TestRemoteRecoveryDoesNotPublishBesideASurvivingWriter(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent/pi", "agent/review"} {
		t.Run(kind, func(t *testing.T) {
			a, fake, script := scriptTaskFixture(t,
				map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_BLOCK": "1"}},
				schema.Step{Name: "agent", Uses: kind, With: map[string]any{"prompt": "work", "executor": "docker"}})
			ctx := context.Background()
			a.cfg.agentExecutor = ExecutorDocker
			daemon := newSurvivingContainers()
			a.docker = docker.Scope{Client: daemon}
			var survivor string
			installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
				survivor = req.Name
				daemon.survive(req)
				return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("agent remains alive")}
			})
			state := onWorker(t, a, fake)
			key := taskKeyOf(script)
			t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
			a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
			if err := a.Work.ExecuteStep(a.asEngine(ctx), script.ID); err != nil {
				t.Fatal(err)
			}
			attempt, err := a.tasks.Records.Get(ctx, key)
			if err != nil || attempt.Source == "" || attempt.Observed != tasks.Started {
				t.Fatalf("remote fixture: %+v %v", attempt, err)
			}
			snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			agent := snap.Steps[1]
			if err := a.Work.ExecuteStep(a.asEngine(ctx), agent.ID); err != nil {
				t.Fatal(err)
			}
			current, err := workflow.ReadStepIn(ctx, a.DB, agent.ID)
			if err != nil || current.Status != workflow.StatusError || !daemon.has(survivor) {
				t.Fatalf("survivor fixture: %+v %v", current, err)
			}
			if err := os.WriteFile(filepath.Join(state, "workspaces", key, "remote-made.txt"), []byte("remote result\n"), 0600); err != nil {
				t.Fatal(err)
			}
			fake.Exit(key, 0)
			e, err := a.Work.RecoverExecution(a.asEngine(ctx), script.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			a.tasks.AbandonAt = nil
			a.workDependencies().Recoverers["script"](e)
			source := workflowSourceForTest(t, a, snap.Run)
			made := filepath.Join(source.Workspace(a.cfg.dataDir), "remote-made.txt")
			if _, err := os.Stat(made); err == nil || !daemon.has(survivor) {
				t.Fatalf("recovery fast-forwarded the shared folder while %s still writes it (survivor live: %v)", kind, daemon.has(survivor))
			}
			// The control: once the survivor can be removed, the same
			// recovery removes it and brings the remote result into the
			// folder, so the refusal above is the surviving writer's doing.
			daemon.allowRemoval()
			if e, err = a.Work.RecoverExecution(a.asEngine(ctx), script.ID, 1); err != nil {
				t.Fatal(err)
			}
			a.workDependencies().Recoverers["script"](e)
			if _, err := os.Stat(made); err != nil || daemon.has(survivor) {
				t.Fatalf("recovery did not publish once the writer was gone: %v (survivor live: %v)", err, daemon.has(survivor))
			}
		})
	}
}

// An unavailable ownership read is not evidence that a still-current task
// lost its attempt. Recovery must retain it so that a later read can decide.
func TestARecoveryReadFailureKeepsTheTask(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"created", "started"} {
		for _, read := range []string{"StepByID", "RunByID"} {
			t.Run(boundary+"/"+read, func(t *testing.T) {
				a, fake, step := fakeTaskFixture(t, true)
				ctx := context.Background()
				key := taskKeyOf(step)
				t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
				a.tasks.AbandonAt = func(stage string) bool { return stage == boundary }
				if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
					t.Fatal(err)
				}
				e, err := a.Work.RecoverExecution(a.asEngine(ctx), step.ID, 1)
				if err != nil {
					t.Fatal(err)
				}
				before, err := a.tasks.Records.Get(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				dbtest.FailNextStatement(t, a.DB, "-- name: "+read+" ")
				a.workDependencies().Recoverers["script"](e)
				after, err := a.tasks.Records.Get(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				current, err := workflow.ReadStepIn(ctx, a.DB, step.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.Status != workflow.StatusRunning || current.Attempt != 1 {
					t.Fatalf("fixture lost current step: %+v", current)
				}
				if after.StopWanted || after.Observed == tasks.Settled || !fake.Present(key) {
					t.Fatalf("failed %s read destroyed a current %s task: before=%s after=%s stop=%v present=%v step=%s", read, boundary, before.Observed, after.Observed, after.StopWanted, fake.Present(key), current.Status)
				}
			})
		}
	}
}

// All three moved source reads precede Claim. A transient read error must
// leave the same pending attempt, with no model call or task start.
func TestASourceReadFailureLeavesAWriterPending(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"script", "agent/pi", "agent/review"} {
		t.Run(kind, func(t *testing.T) {
			a, fake, script := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "image": "fake"}, schema.Step{Name: "agent", Uses: kind})
			ctx := context.Background()
			pi := installFakePi(t, a, func(_ int, _ ExecRequest) ExecOutcome { return ExecOutcome{Output: piSays("unexpected")} })
			snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			step := snap.Steps[1]
			if kind == "script" {
				step = script
			}
			dbtest.FailNextStatement(t, a.DB, "name: WorkflowSource")
			if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
				t.Fatal(err)
			}
			got, err := workflow.ReadStepIn(ctx, a.DB, step.ID)
			if err != nil || got.Status != workflow.StatusPending || got.Attempt != step.Attempt || len(pi.requests()) != 0 || fake.Starts(taskKeyOf(step)) != 0 {
				t.Fatalf("failed source read admitted work: %+v err=%v", got, err)
			}
		})
	}
}

// Fresh waiters must recheck admission after either resource wait.
func TestAWaiterWhoseRunStoppedDoesNotLaunch(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"script", "agent/pi", "agent/review"} {
		for _, capacity := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/capacity=%d", kind, capacity), func(t *testing.T) {
				a, fake, script := scriptTaskFixture(t,
					map[string]any{"run": "true", "executor": "docker", "image": "fake"},
					schema.Step{Name: "agent", Uses: "agent/pi", With: map[string]any{"prompt": "hold the folder", "executor": "local"}},
					schema.Step{Name: "incoming", Uses: kind, With: map[string]any{"prompt": "waiting", "executor": "local"}})
				ctx := context.Background()
				snap, err := a.Work.Snapshot(a.asEngine(ctx), script.RunID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				agent := snap.Steps[1]
				if kind != "script" {
					script = snap.Steps[2]
				}
				a.cfg.agentExecutor = ExecutorLocal
				a.capacity = models.NewScheduler(nil, capacity)
				calls := 0
				entered, release := make(chan struct{}), make(chan struct{})
				released := false
				defer func() {
					if !released {
						close(release)
					}
				}()
				installFakePi(t, a, func(_ int, _ ExecRequest) ExecOutcome {
					calls++
					if calls > 1 {
						return ExecOutcome{Output: piSays("unexpected")}
					}
					close(entered)
					<-release
					return ExecOutcome{Output: piSays("done")}
				})
				agentDone := make(chan error, 1)
				go func() { agentDone <- a.Work.ExecuteStep(a.asEngine(ctx), agent.ID) }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("agent did not enter")
				}
				scriptDone := make(chan error, 1)
				go func() { scriptDone <- a.Work.ExecuteStep(a.asEngine(ctx), script.ID) }()
				// Prove the capacity wait when the holder occupies the only slot:
				// once the step is witnessed waiting, a look shows it has not
				// returned. Without that witness, a bounded negative
				// observation, as in the existing folder-wait regression.
				// Source inspection establishes where the mutex is acquired;
				// returning here would mean it did not wait.
				if kind != "script" && capacity == 1 {
					if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
						_, _, waiting := a.capacity.Waiting(script.ID)
						return waiting
					}) {
						t.Fatal("incoming agent never waited for capacity")
					}
					select {
					case err := <-scriptDone:
						t.Fatalf("incoming step returned while it waited for capacity: %v", err)
					default:
					}
				} else {
					select {
					case err := <-scriptDone:
						t.Fatalf("incoming step returned while folder was held: %v", err)
					case <-time.After(300 * time.Millisecond):
					}
				}
				if step, err := workflow.ReadStepIn(ctx, a.DB, script.ID); err != nil || step.Status == workflow.StatusRunning {
					t.Fatalf("the incoming step claimed while another step held the folder: %+v %v", step, err)
				}
				if fake.Starts(taskKeyOf(script)) != 0 {
					t.Fatal("script did not wait for the folder")
				}
				if _, err := a.Work.Abandon(a.asEngine(ctx), workflow.StopWork{RunID: script.RunID, Reason: "cancel while queued"}); err != nil {
					t.Fatal(err)
				}
				run, err := workflow.ReadRunIn(ctx, a.DB, script.RunID)
				if err != nil || run.Status == workflow.RunRunning {
					t.Fatalf("abandon did not commit: %+v %v", run, err)
				}
				close(release)
				released = true
				for _, done := range []chan error{agentDone, scriptDone} {
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("writer did not finish")
					}
				}
				if n := fake.Starts(taskKeyOf(script)); n != 0 {
					t.Fatalf("abandoned script started %d time(s) after acquiring the folder lock", n)
				}

				if calls != 1 {
					t.Fatalf("stopped %s launched after waiting: %d model calls", kind, calls)
				}
			})
		}
	}
}

type unstoppableTasks struct {
	execution.TaskDriver
	workspaces map[string]string
}

func (d *unstoppableTasks) Stop(context.Context, execution.TaskRef) error {
	return errors.New("Docker did not confirm the stop")
}

func (d *unstoppableTasks) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	d.workspaces[spec.Key] = spec.Workspace
	return d.TaskDriver.Ensure(ctx, spec)
}

// A script task that timed out while AppTown was down, and whose stop the
// backend did not confirm, stays unsettled and may still write the change's
// folder. Neither a retry of its step nor the next step starts beside it.
func TestNoAttemptStartsBesideAScriptTaskThatHasNotStopped(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0", "FAKE_BLOCK": "1"}},
		schema.Step{Name: "check", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0"}}})
	ctx := context.Background()
	driver := &unstoppableTasks{TaskDriver: fake, workspaces: map[string]string{}}
	a.tasks.Driver = driver
	first := taskKeyOf(step)
	second := fmt.Sprintf("step-%d-attempt-2", step.ID)
	t.Cleanup(func() {
		_ = fake.Release(ctx, execution.TaskRef{Key: first})
		_ = fake.Release(ctx, execution.TaskRef{Key: second})
	})
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the recorded deadline passes during the outage, without waiting out the ten-minute budget.
	if _, err := a.DB.Exec(`UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=?`, first); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a)
	b.Work.Wait()
	b.Work.Stop()
	a = b
	current, err := workflow.ReadStepIn(ctx, a.DB, step.ID)
	if err != nil || current.Status != workflow.StatusError {
		t.Fatalf("step: %+v %v", current, err)
	}
	attempt, err := a.tasks.Records.Get(ctx, first)
	if err != nil || attempt.Observed == tasks.Settled {
		t.Fatalf("attempt: %+v %v", attempt, err)
	}
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: first})
	if err != nil || obs.State != execution.TaskRunning {
		t.Fatalf("old task: %+v %v", obs, err)
	}
	// The next step in the stage does not start beside it.
	snap, err := a.Work.Snapshot(a.asEngine(ctx), step.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name != "check" {
			continue
		}
		// The restarted AppTown may already have dispatched it.
		_ = a.Work.ExecuteStep(a.asEngine(ctx), s.ID)
		later, err := workflow.ReadStepIn(ctx, a.DB, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if later.Status != workflow.StatusError || len(driver.workspaces) != 1 {
			t.Fatalf("the next step started beside the unstopped task: %s, tasks started=%v", later.Status, driver.workspaces)
		}
	}
	if _, err := a.Work.RetryStep(a.asEngine(ctx), workflow.RetryCommand{StepID: step.ID}); err != nil {
		return
	}
	// Leave the replacement at its started boundary, so the test can inspect
	// both providers without waiting for either deliberately blocked task.
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
		t.Fatal(err)
	}
	next, err := fake.Observe(ctx, execution.TaskRef{Key: second})
	if err != nil {
		t.Fatal(err)
	}
	if next.State == execution.TaskRunning {
		if driver.workspaces[first] == "" || driver.workspaces[first] != driver.workspaces[second] {
			t.Fatal("fixture did not use one folder")
		}
		t.Fatalf("two attempts write %s: first=%s (%s, stop=%v), second=%s", driver.workspaces[first], obs.State, attempt.Observed, attempt.StopWanted, next.State)
	}
}

// A container writing the folder whose step no longer exists is a leftover
// like any other: under the folder lock no other step is working there, so
// the attempt removes it before starting, and refuses, starting nothing, if
// it cannot be removed. (Before the lock this refused whenever the step could
// not be read.)
func TestAWriterWhoseStepIsGoneIsRemovedOrRefusesTheAttempt(t *testing.T) {
	t.Parallel()
	for _, removable := range []bool{true, false} {
		t.Run(fmt.Sprintf("removable=%v", removable), func(t *testing.T) {
			a := agentTestApp(t)
			a.cfg.agentExecutor = ExecutorDocker
			d := newSurvivingContainers()
			if removable {
				d.allowRemoval()
			}
			a.docker = docker.Scope{Client: d}
			runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it"}`)
			run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
			if err != nil {
				t.Fatal(err)
			}
			source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: runID, ProjectID: run.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			d.survive(ExecRequest{Name: "orphan-writer", Labels: []string{docker.WritesLabel + "=" + source.Workspace(a.cfg.dataDir), "forge.step=9999999"}})
			calls := 0
			installFakePi(t, a, func(_ int, _ ExecRequest) ExecOutcome { calls++; return ExecOutcome{Output: piSays("done")} })
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
				t.Fatal(err)
			}
			got, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case removable && (calls != 1 || d.has("orphan-writer")):
				t.Fatalf("the leftover was not removed before the agent ran: calls=%d left=%v", calls, d.has("orphan-writer"))
			case !removable && (calls != 0 || got.Status != workflow.StatusError || !d.has("orphan-writer")):
				t.Fatalf("an agent started beside a leftover it could not remove: calls=%d status=%s", calls, got.Status)
			}
		})
	}
}

// A container writing the folder with no step label is never taken for a
// step at work: an attempt removes it first, or does not start.
func TestAWriterWithNoStepIsRemovedBeforeAnAttempt(t *testing.T) {
	t.Parallel()
	for _, removable := range []bool{false, true} {
		t.Run(fmt.Sprint(removable), func(t *testing.T) {
			a := agentTestApp(t)
			a.cfg.agentExecutor = ExecutorDocker
			d := newSurvivingContainers()
			if removable {
				d.allowRemoval()
			}
			a.docker = docker.Scope{Client: d}
			runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it"}`)
			run, err := a.Work.Run(a.asEngine(context.Background()), runID)
			if err != nil {
				t.Fatal(err)
			}
			source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: runID, ProjectID: run.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			d.survive(ExecRequest{Name: "no-step-label", Labels: []string{docker.WritesLabel + "=" + source.Workspace(a.cfg.dataDir)}})
			calls := 0
			installFakePi(t, a, func(_ int, _ ExecRequest) ExecOutcome { calls++; return ExecOutcome{Output: piSays("done")} })
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
				t.Fatal(err)
			}
			if removable {
				if calls != 1 || d.has("no-step-label") {
					t.Fatalf("removable: calls=%d survives=%v", calls, d.has("no-step-label"))
				}
			} else {
				if calls != 0 || !d.has("no-step-label") {
					t.Fatalf("unremovable: calls=%d survives=%v", calls, d.has("no-step-label"))
				}
			}
		})
	}
}

// A restart resets the whole folder, so it does not start while another
// step's container (here a reviewer whose timeout left it behind) may still
// write there; nor does any other attempt of a step.
func TestARestartWaitsForASiblingStepLeftWritingTheFolder(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	a.cfg.agentExecutor = ExecutorDocker
	d := newSurvivingContainers()
	a.docker = docker.Scope{Client: d}
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "sibling", Name: "sibling", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "sibling", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
			{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "do it"}},
			{Name: "review", Uses: "agent/review"},
		}}},
	}})
	a.Work.Stop()
	ch, err := a.Work.Create(asPerson("alice"), workflow.ChangeDraft{ProjectID: p.ID, Title: "sibling review"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asPerson("alice"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "sibling"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snap, err := a.Work.Snapshot(asPerson("alice"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var build, review ids.ID
	for _, s := range snap.Steps {
		if s.Name == "build" {
			build = s.ID
		}
		if s.Name == "review" {
			review = s.ID
		}
	}
	if build.IsZero() || review.IsZero() {
		t.Fatal("steps missing")
	}
	calls := 0
	installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
		calls++
		if strings.HasPrefix(req.Name, "apptown-review-") {
			d.survive(req)
			return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("reviewer still writing")}
		}
		return ExecOutcome{Output: piSays("implemented")}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), build)
	if s, ev := stepStatus(t, a, build); s != workflow.StatusPass {
		t.Fatalf("build fixture: %s %v", s, ev)
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), review)
	if s, _ := stepStatus(t, a, review); s != workflow.StatusError {
		t.Fatalf("review fixture: %s", s)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(source.Workspace(a.cfg.dataDir), "reviewer-work.txt")
	if err = os.WriteFile(marker, []byte("still being written"), 0600); err != nil {
		t.Fatal(err)
	}
	before := calls
	if _, err = a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: build, Mode: workflow.RetryRestart}); err != nil {
		return
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), build)
	_, statErr := os.Stat(marker)
	if calls != before || os.IsNotExist(statErr) {
		t.Fatalf("restart launched %d replacement(s), marker exists=%v, while sibling reviewer survives", calls-before, statErr == nil)
	}
}

// An agent with a private Docker daemon is not re-run in place after an
// empty turn: what it started there would still be running beside the
// re-run.
func TestAnAgentWithAPrivateDockerIsNotRerunInPlace(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	a.cfg.agentExecutor = ExecutorDocker
	if _, err := a.Ways.CreateBlock(t.Context(), "skill/implementation", "Implement.", "", "alice"); err != nil {
		t.Fatal(err)
	}
	d := newSurvivingContainers()
	a.docker = docker.Scope{Client: d}
	_, step := admittedAgentStep(t, a, `{"prompt":"do it","skills":["skill/implementation"],"docker":true}`)
	calls := 0
	installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
		calls++
		if envValue(req, "DOCKER_HOST") == "" {
			t.Fatal("private daemon not provisioned")
		}
		if len(d.live) == 0 {
			t.Fatal("private daemon missing")
		}
		// Pi exits with an empty assistant turn; work started through its
		// private daemon can remain detached. Only removing dind stops it.
		return ExecOutcome{Output: piSays("")}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), step)
	if calls != 1 {
		t.Fatalf("launched %d agents against the same surviving private daemon", calls)
	}
}

type capturedTask struct {
	execution.TaskDriver
	spec execution.TaskSpec
}

func (d *capturedTask) Ensure(ctx context.Context, s execution.TaskSpec) (execution.TaskObservation, error) {
	d.spec = s
	return d.TaskDriver.Ensure(ctx, s)
}

// A script task that mounts the change's folder writable is labelled as its
// writer and with its step, so the folder's writers can be found.
func TestAScriptTaskIsLabelledAsItsFoldersWriter(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	d := &capturedTask{TaskDriver: fake}
	a.tasks.Driver = d
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	s := d.spec
	if s.Workspace == "" || s.WorkspaceRO {
		t.Fatalf("fixture did not mount writable folder: %+v", s)
	}
	for _, want := range []string{docker.WritesLabel + "=" + s.Workspace, fmt.Sprintf("forge.step=%d", step.ID)} {
		if !slices.Contains(s.Labels, want) {
			t.Errorf("writable script task omits %q; labels=%v", want, s.Labels)
		}
	}
}

// A timed-out advisory review can leave a runtime and a stale running status
// when its outcome write fails. The declared gate permits advancement, but
// that old stage is not work legitimately running beside the next stage.
func TestAStepLeftMarkedRunningDoesNotShieldItsContainer(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	a.cfg.agentExecutor = ExecutorDocker
	d := newSurvivingContainers()
	a.docker = docker.Scope{Client: d}
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "stale-running", Name: "stale-running", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "reviews", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "check", Steps: []schema.Step{{Name: "ready", Uses: "agent/review"}, {Name: "first", Uses: "agent/review"}}, Gate: &schema.Gate{Require: []string{"ready"}}},
		{Name: "next", Steps: []schema.Step{{Name: "second", Uses: "agent/pi", With: map[string]any{"prompt": "do it"}}}},
	}}})
	a.Work.Stop()
	ch, err := a.Work.Create(asPerson("alice"), workflow.ChangeDraft{ProjectID: p.ID, Title: "two reviews"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asPerson("alice"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "reviews"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	var old string
	calls := 0
	installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
		calls++
		if calls == 2 {
			old = req.Name
			d.survive(req)
			// The outcome and the error that would replace it both fail to
			// record: the step is left marked running, as a crash leaves it.
			a.agents.Settle = workflow.Settlement{Tries: 1}
			t.Cleanup(a.DB.FailStatements("-- name: FinishAttempt", 2))
			return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("old review survives")}
		}
		writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"checked","findings":[]}`)
		return ExecOutcome{Output: piSays("checked")}
	})
	ready := reviewExecute(t, a, run, "ready")
	if ready.Status != workflow.StatusPass {
		t.Fatalf("ready: %s %s", ready.Status, ready.Evidence)
	}
	first := reviewExecute(t, a, run, "first")
	if first.Status != workflow.StatusRunning || calls != 2 || !d.has(old) {
		t.Fatalf("fixture: status=%s calls=%d old=%v", first.Status, calls, d.has(old))
	}
	advanced, err := a.Work.AdvanceFrom(asPerson("alice"), run)
	if err != nil || !advanced.Advanced {
		t.Fatalf("advance past advisory review: %+v %v", advanced, err)
	}
	second := reviewExecute(t, a, run, "second")
	if calls == 2 {
		return
	} // refusal is the required result
	if second.Status != workflow.StatusPass {
		t.Fatalf("replacement: %s %s", second.Status, second.Evidence)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(source.Workspace(a.cfg.dataDir), "old-review-work.txt")
	if err := os.WriteFile(marker, []byte("still writing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: second.ID, Mode: workflow.RetryRestart}); err != nil {
		t.Fatal(err)
	}
	reviewExecute(t, a, run, "second")
	_, statErr := os.Stat(marker)
	t.Fatalf("next-stage agent and its restart ran beside the old reviewer: calls=%d oldPresent=%v markerPresent=%v staleStatus=%s", calls, d.has(old), statErr == nil, first.Status)
}

// The early holder check (a checkout's, a landing's) removes nothing, not
// even a chat runtime left behind after its record was lost and the startup
// reaper could not remove it; only a take under the branch's lock removes.
func TestTheEarlyHolderCheckRemovesNothing(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := w.a.Console.Send(context.Background(), id, "do it"); err != nil {
		t.Fatal(err)
	}
	sess, err := w.a.Console.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// Fault injection: the session's metadata is lost at process restart.
	if err := os.Remove(filepath.Join(sess.Dir, console.MetaFile)); err != nil {
		t.Fatal(err)
	}
	w.a.Console = console.New(w.a.consoleDependencies(failedReaper{w.agent}, nil))
	rebuildConsoleOwners(w.a)
	if err := w.a.RecoverConsoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := w.agent.Status(context.Background(), id)
	if err != nil || !before.Exists {
		t.Fatalf("fixture: no orphan runtime: %+v %v", before, err)
	}
	_, err = workflow.HolderRecordsEndedIn(context.Background(), w.a.DB, w.change.ProjectID, scm.ChatHolder(id), holderPresenceFor(w.a), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	after, err := w.agent.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Exists {
		t.Fatal("record-only early check removed the orphan chat runtime without a branch lock")
	}
}

// A retry of an agent step, continuing or restarting, does not start (nor
// reset the folder) while an earlier attempt's agent is still there.
func TestARetryWaitsForTheEarlierAgentsContainer(t *testing.T) {
	t.Parallel()
	for _, mode := range []workflow.RetryMode{workflow.RetryContinue, workflow.RetryRestart} {
		t.Run(mode.String(), func(t *testing.T) {
			a := agentTestApp(t)
			a.cfg.agentExecutor = ExecutorDocker
			daemon := newSurvivingContainers()
			a.docker = docker.Scope{Client: daemon}
			_, stepID := admittedAgentStep(t, a, `{"prompt":"do it"}`)
			calls := 0
			installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
				calls++
				daemon.survive(req)
				return ExecOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("old container still writes")}
			})
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			if status, _ := stepStatus(t, a, stepID); status != workflow.StatusError {
				t.Fatalf("first attempt: %s", status)
			}
			if _, err := a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: stepID, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			if status, ev := stepStatus(t, a, stepID); calls != 1 || status != workflow.StatusError {
				t.Fatalf("%d agent invocations while the first is still there (%s %v)", calls, status, ev)
			}
		})
	}
}

// survivingContainers is a Docker daemon holding the containers a test says
// survived: those it ran itself and those recorded with survive. It lists
// them by label, as Docker does, and refuses to remove them until allowed.
type survivingContainers struct {
	mu        sync.Mutex
	live      map[string][]string // name → labels
	removable bool
}

func newSurvivingContainers() *survivingContainers {
	return &survivingContainers{live: map[string][]string{}}
}

// survive records an agent's container as still there, labelled as the
// runner labels it.
func (d *survivingContainers) survive(req ExecRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.live[req.Name] = append([]string{"forge.class=step"}, req.Labels...)
}

func (d *survivingContainers) allowRemoval() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removable = true
}

func (d *survivingContainers) has(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.live[name]
	return ok
}

func (d *survivingContainers) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	args := req.Args
	switch {
	case len(args) == 0:
		return nil, nil
	case args[0] == "run" || args[0] == "create":
		var name string
		var labels []string
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "--name":
				name = args[i+1]
			case "--label":
				labels = append(labels, args[i+1])
			}
		}
		if name != "" {
			d.live[name] = labels
		}
		return []byte(name), nil
	case args[0] == "ps":
		var want []string
		format := ""
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "--filter":
				want = append(want, strings.TrimPrefix(args[i+1], "label="))
			case "--format":
				format = args[i+1]
			}
		}
		// A format naming a label ({{.Label "key"}}) gets that label's value
		// after a tab, as Docker prints it.
		key := ""
		if _, rest, ok := strings.Cut(format, `.Label "`); ok {
			key, _, _ = strings.Cut(rest, `"`)
		}
		var lines []string
		for name, labels := range d.live {
			if slices.ContainsFunc(want, func(w string) bool { return !slices.Contains(labels, w) }) {
				continue
			}
			line := name
			if key != "" {
				value := ""
				for _, l := range labels {
					if k, v, _ := strings.Cut(l, "="); k == key {
						value = v
					}
				}
				line += "\t" + value
			}
			lines = append(lines, line)
		}
		return []byte(strings.Join(lines, "\n")), nil
	case args[0] == "rm":
		if !d.removable {
			return []byte("daemon did not answer"), errors.New("exit status 1")
		}
		delete(d.live, args[len(args)-1])
		return nil, nil
	case args[0] == "image":
		return nil, errors.New("no image here")
	}
	return nil, nil
}
