package apptown

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// fakeTaskFixture admits one script step whose task runs on the fake driver.
// block makes the task run until the test ends it.
func fakeTaskFixture(t *testing.T, block bool) (*App, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	env := map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0"}
	if block {
		env["FAKE_BLOCK"] = "1"
	}
	return scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": env})
}

// selectionTaskFixture is fakeTaskFixture for a test that points the
// process's Docker selection (DOCKER_*) at a daemon that does not exist.
// Taking a change asks Docker which containers write its folder; the
// integration tier's default Docker asks the real CLI, which would follow
// that selection and fail, so these apps answer that question themselves in
// every tier (restarts: restartAfterSelection).
func selectionTaskFixture(t *testing.T) (*App, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	env := map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0"}
	return scriptTaskFixtureIn(t, testApp(t, Config{DockerClient: noFolderWriters{}}), map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": env})
}

// restartAfterSelection is an AppTown restarted on a's data under the Docker
// selection the process has now, answering the folder-writer question as
// selectionTaskFixture does.
func restartAfterSelection(t *testing.T, a *App) *App {
	t.Helper()
	return newApp(t, a.DB, Config{DataDir: a.cfg.dataDir, DockerClient: noFolderWriters{}})
}

// noFolderWriters is the real Docker CLI except that it lists no container
// as writing a change's folder: the default tier's Docker (whose tests must
// not depend on what runs on the machine, or on a daemon at all), and the
// Docker of tests whose selection names no daemon.
type noFolderWriters struct{ docker.CLI }

func (c noFolderWriters) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	if len(req.Args) > 0 && req.Args[0] == "ps" && slices.ContainsFunc(req.Args, func(a string) bool { return strings.HasPrefix(a, "label="+docker.WritesLabel+"=") }) {
		return nil, nil
	}
	return c.CLI.Run(ctx, host, req)
}

// scriptTaskFixture admits one script step configured by with (and any
// further steps in the same stage after it).
func scriptTaskFixture(t *testing.T, with map[string]any, more ...schema.Step) (*App, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	return scriptTaskFixtureIn(t, testApp(t), with, more...)
}

// scriptTaskFixtureIn is scriptTaskFixture on a.
func scriptTaskFixtureIn(t *testing.T, a *App, with map[string]any, more ...schema.Step) (*App, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	fake := &executionfake.Tasks{}
	a.tasks.Driver, a.tasks.Poll = fake, 20*time.Millisecond
	projectID := plainTestProject(t, a.DB, "fake-tasks", "Fake Tasks")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "build", Steps: append([]schema.Step{{Name: "make", Uses: "script", With: with}}, more...)},
	}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: spec})
	a.lifecycle.SelectLane(t.Context(), projectID, "work", true, "fixture")
	ctx := a.asAppTown(context.Background(), projectID, "fixture")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "fake tasks"})
	if err == nil {
		ch, err = a.Work.Begin(ctx, ch.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1+len(more) {
		t.Fatalf("script steps: %+v %v", snapshot, err)
	}
	return a, fake, snapshot.Steps[0]
}

func taskKeyOf(step *workflow.Step) string { return fmt.Sprintf("step-%d-attempt-1", step.ID) }

// restarted is AppTown after a restart: a new App on a's database and data
// directory, its tasks on the same backend, having run startup repair and
// resumed recorded work, as the daemon does. a must be stopped.
func restarted(t *testing.T, a *App) *App {
	t.Helper()
	b := newApp(t, a.DB, Config{DataDir: a.cfg.dataDir})
	b.tasks.Driver, b.tasks.Remote, b.tasks.Profile, b.tasks.Backend, b.tasks.Poll = a.tasks.Driver, a.tasks.Remote, a.tasks.Profile, a.tasks.Backend, a.tasks.Poll
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(context.Background())
	return b
}

func TestARestartNeverStartsARecordedTaskAgain(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ backend, stage string }{{"local", "created"}, {"local", "started"}, {"worker", "created"}, {"worker", "started"}} {
		t.Run(c.backend+"/"+c.stage, func(t *testing.T) {
			t.Parallel()
			stage := c.stage
			a, fake, step := fakeTaskFixture(t, true)
			if c.backend == "worker" {
				onWorker(t, a, fake)
			}
			a.tasks.AbandonAt = func(s string) bool { return s == stage }
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
				t.Fatal(err)
			}
			a.tasks.AbandonAt = nil
			b := restarted(t, a)
			key := taskKeyOf(step)
			poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return fake.Starts(key) > 0 })
			fake.Exit(key, 0)
			awaitStep(t, b, step.ID, workflow.StatusPass)
			if starts := fake.Starts(key); starts != 1 {
				t.Fatalf("the task was started %d times", starts)
			}
		})
	}
}

// drained waits for everything the App's Work has dispatched to finish:
// after it, no dispatched step is still going to act. It fails the test
// rather than hang.
func drained(t *testing.T, a *App) {
	t.Helper()
	done := make(chan struct{})
	go func() { a.Work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("AppTown's dispatched work did not finish")
	}
}

// awaitStep waits for the step to reach want, reading it as Work records it.
func awaitStep(t *testing.T, a *App, id ids.ID, want workflow.StepState) *workflow.Step {
	t.Helper()
	var step *workflow.Step
	var err error
	if !poll.Until(context.Background(), 90*time.Second, 10*time.Millisecond, func() bool {
		step, err = workflow.ReadStepIn(context.Background(), a.DB, id)
		return err != nil || step.Status == want
	}) {
		t.Fatalf("step %d is %s, want %s: %s", id, step.Status, want, step.Evidence)
	}
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// onWorker moves the fixture's tasks to a worker served in this process over
// mutual TLS, with the fake behind it, as an installation configured with a
// worker does. It returns the worker's state directory.
func onWorker(t *testing.T, a *App, fake *executionfake.Tasks) string {
	t.Helper()
	ca, state := workertest.NewCA(t), t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, fake))
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	return state
}

// startOnWorker runs the fixture's step until its task is running on the
// worker, and returns the run and the task's workspace there.
func startOnWorker(t *testing.T) (*App, *executionfake.Tasks, *workflow.Step, *workflow.Run, string) {
	t.Helper()
	a, fake, step := fakeTaskFixture(t, true)
	state := onWorker(t, a, fake)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	executeUntilStarted(t, a, fake, step.ID, key)
	return a, fake, step, run, filepath.Join(state, "workspaces", key)
}

// executeUntilStarted runs a step (ExecuteStep, to its end) and waits up to
// ten seconds for its task, key, to start on the worker; the test's cleanup
// ends the task (exit 137) and waits for the step. A step whose task never
// starts fails the test at once and is not waited for: the product asks a
// worker that does not answer again until the attempt's deadline (ten
// minutes by default), whatever its caller's context, and a test must not
// sit that out. The reason is in the log ("asking again").
func executeUntilStarted(t *testing.T, a *App, fake *executionfake.Tasks, stepID ids.ID, key string) {
	t.Helper()
	executed := make(chan error, 1)
	go func() { executed <- a.Work.ExecuteStep(a.asEngine(context.Background()), stepID) }()
	started := false
	t.Cleanup(func() {
		fake.Exit(key, 137) // a test that stopped early leaves nothing waiting
		wait := time.NewTimer(time.Hour)
		if !started {
			wait.Reset(10 * time.Second)
		}
		defer wait.Stop()
		select {
		case err := <-executed:
			if err != nil && started {
				t.Error(err)
			}
		case <-wait.C:
			t.Log("the step, whose task never started, is left asking the worker until its deadline")
		}
	})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for fake.Starts(key) == 0 {
		select {
		case err := <-executed:
			executed <- err
			t.Fatalf("the step ended before its task started on the worker: %v", err)
		case <-deadline.C:
			t.Fatal("the task never started on the worker within 10s: the worker did not take it (see \"asking again\" above)")
		case <-time.After(10 * time.Millisecond):
		}
	}
	started = true
}

// bindNodeMirror binds a Node.js dist mirror: a registry that changes only
// the generated mise configuration, not the step's environment.
func bindNodeMirror(t *testing.T, a *App, url string) {
	t.Helper()
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), "node-mirror", "registry", "registry", `{"format":"node-dist","url":"`+url+`"}`, false); err != nil {
		t.Fatal(err)
	}
}

// The tests below stay with the composition root: the backend a task is
// recorded against is chosen when the App is built, from the Docker
// selection in its environment (taskNamespace and execution.SelectDocker in
// New). The task runner's own recovery guarantees are workflowexec's.

// DOCKER_CONTEXT selects a daemon even when DOCKER_HOST is unchanged. Model
// the two daemons with independent providers, using production App identities.
func TestAChangedDockerContextDoesNotMoveAnAttempt(t *testing.T) {
	// Not parallel: t.Setenv selects the Docker daemon through DOCKER_* in the process environment.
	// The selection is the real CLI's; no daemon is needed, but the CLI is
	// (CI's Go jobs install it; a machine without it skips, like the Docker
	// package's real-CLI test).
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the Docker CLI is not installed")
	}
	_ = testDB(t) // initialize the real local test database before changing Docker selection
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "first-daemon")
	a, first, step := selectionTaskFixture(t)
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	ctx := context.Background()
	if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	original, err := tasks.New(a.DB).Get(ctx, key)
	if err != nil || original.ProviderID != "" || !first.Present(key) {
		t.Fatalf("fixture: %+v %v", original, err)
	}
	t.Setenv("DOCKER_CONTEXT", "second-daemon")
	b := restartAfterSelection(t, a)
	second := &executionfake.Tasks{}
	b.tasks.Driver, b.tasks.Poll = second, a.tasks.Poll
	if err := b.RecoverInterruptedWork(ctx); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(ctx)
	b.Work.Wait()
	if second.Starts(key) != 0 {
		t.Fatalf("changed Docker context ran admitted work on another daemon: old=%q new=%q starts=%d", original.Backend, b.tasks.Backend, second.Starts(key))
	}
}

func dockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: args})
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Docker resolves these selections through its real CLI, with a private
// config directory. Independent fake providers represent the two endpoints;
// no second daemon is needed to check AppTown's persisted admission identity.
func TestAChangedDockerEndpointDoesNotMoveAnAttempt(t *testing.T) {
	// Not parallel: t.Setenv selects the Docker daemon through DOCKER_* in the process environment.
	// The selection is the real CLI's; no daemon is needed, but the CLI is
	// (CI's Go jobs install it; a machine without it skips, like the Docker
	// package's real-CLI test).
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the Docker CLI is not installed")
	}
	_ = testDB(t) // initialize local PostgreSQL before changing Docker selection
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "review-daemon")
	dockerCLI(t, "context", "create", "review-daemon", "--docker", "host=unix:///tmp/apptown-review-first.sock")
	before := dockerCLI(t, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	a, first, step := selectionTaskFixture(t)
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	ctx := context.Background()
	if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	t.Cleanup(func() { _ = first.Release(context.Background(), execution.TaskRef{Key: key}) })
	original, err := tasks.New(a.DB).Get(ctx, key)
	if err != nil || original.ProviderID != "" || !first.Present(key) {
		t.Fatalf("fixture: %+v %v", original, err)
	}
	dockerCLI(t, "context", "update", "review-daemon", "--docker", "host=unix:///tmp/apptown-review-second.sock")
	after := dockerCLI(t, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if before == after {
		t.Fatalf("fixture did not change the CLI endpoint: %s", before)
	}
	b := restartAfterSelection(t, a)
	second := &executionfake.Tasks{}
	b.tasks.Driver, b.tasks.Poll = second, a.tasks.Poll
	if err := b.RecoverInterruptedWork(ctx); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(ctx)
	b.Work.Wait()
	if second.Starts(key) != 0 {
		t.Fatalf("endpoint %s -> %s moved admitted work: old backend=%q new backend=%q starts=%d", before, after, original.Backend, b.tasks.Backend, second.Starts(key))
	}
}

// A refusal before anything was created settles whichever of its writes
// fails, and however AppTown restarts: the attempt's refusal, the step's
// error, or the attempt's settlement. A restart finishes it without asking
// the refused backend, and still does once the Docker selection is repaired
// and AppTown names another backend. The step ends in error and the
// attempt settled, with nothing held to release; nothing is created.
func TestARefusalSettlesAcrossEveryWriteBoundary(t *testing.T) {
	// Not parallel: its cases t.Setenv the Docker daemon selection (DOCKER_*) in the process environment.
	for _, tc := range []struct {
		name     string
		fail     string // the statement that fails once
		tries    int    // SettleTries; one makes a single failure final
		repaired bool   // the first restart is already on the repaired selection
	}{
		{"the attempt's refusal, retried", "-- name: RecordRefusal", 0, false},
		{"the attempt's refusal", "-- name: RecordRefusal", 1, false},
		{"the step's error", "-- name: FinishAttempt", 1, false},
		{"the step's error, restarted once repaired", "-- name: FinishAttempt", 1, true},
		{"the attempt's settlement", "-- name: SettleNothingHeld", 0, false},
		{"the attempt's settlement, restarted once repaired", "-- name: SettleNothingHeld", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = testDB(t)
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_CONTEXT", "refusal-missing")
			a, _, step := selectionTaskFixture(t)
			daemon := execution.SelectDocker()
			if daemon.Refused == nil {
				t.Fatal("fixture must be refused")
			}
			a.tasks.Driver = execution.DockerTasks{Daemon: daemon}
			a.tasks.SettleTries = tc.tries
			ctx := context.Background()
			dbtest.FailNextStatement(t, a.DB, tc.fail)
			if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
				t.Fatal(err)
			}
			restart := func() *App {
				b := restartAfterSelection(t, a)
				if err := b.RecoverInterruptedWork(ctx); err != nil {
					t.Fatal(err)
				}
				b.ResumePendingAutoSteps(ctx)
				b.Work.Wait()
				return b
			}
			if !tc.repaired {
				restart() // the same refused selection
			}
			t.Setenv("DOCKER_CONTEXT", "default")
			b := restart() // repaired: another backend
			got, err := workflow.ReadStepIn(ctx, a.DB, step.ID)
			if err != nil || got.Status != workflow.StatusError {
				t.Fatalf("step: %+v %v", got, err)
			}
			attempt, err := tasks.New(a.DB).Get(ctx, taskKeyOf(step))
			if err != nil {
				t.Fatal(err)
			}
			// Nothing was created, so nothing is ever released under its key.
			if attempt.Observed != tasks.Settled || attempt.ProviderID != "" || !attempt.Released {
				t.Fatalf("the refused attempt is %s (provider %q): backend=%q current=%q", attempt.Observed, attempt.ProviderID, attempt.Backend, b.tasks.Backend)
			}
		})
	}
}

// A step refused by a Docker selection that could not be pinned settles its
// attempt as it fails: it made nothing, so it is not left unknown for a
// recovery that, once the selection is repaired, would never match its
// backend.
func TestARefusedDockerSelectionSettlesItsAttempt(t *testing.T) {
	// Not parallel: t.Setenv selects the Docker daemon through DOCKER_* in the process environment.
	// The selection is the real CLI's; no daemon is needed, but the CLI is
	// (CI's Go jobs install it; a machine without it skips, like the Docker
	// package's real-CLI test).
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the Docker CLI is not installed")
	}
	_ = testDB(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "round6-missing")
	a, _, step := selectionTaskFixture(t)
	daemon := execution.SelectDocker()
	if daemon.Refused == nil {
		t.Fatal("fixture must be refused")
	}
	a.tasks.Driver = execution.DockerTasks{Daemon: daemon}
	ctx := context.Background()
	if err := a.Work.ExecuteStep(a.asEngine(ctx), step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(ctx, a.DB, step.ID)
	if err != nil || got.Status != workflow.StatusError {
		t.Fatalf("step: %+v %v", got, err)
	}
	// Repair configuration and construct the production recovery path.
	t.Setenv("DOCKER_CONTEXT", "default")
	b := restartAfterSelection(t, a)
	if err := b.RecoverInterruptedWork(ctx); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(ctx)
	b.Work.Wait()
	attempt, err := tasks.New(a.DB).Get(ctx, taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Observed != tasks.Settled {
		t.Fatalf("known pre-effect refusal remains %s: backend=%q current=%q step=%s provider=%q", attempt.Observed, attempt.Backend, b.tasks.Backend, got.Status, attempt.ProviderID)
	}
}
