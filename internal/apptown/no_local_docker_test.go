package apptown

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// The no-local-Docker mode.

// ownDockerTripwire is the Docker client given to an AppTown in the mode. The
// mode wraps it (docker.Refusing), so only an attached target's call, which
// names its host, may reach it; a call without a host fails the test.
type ownDockerTripwire struct{ t *testing.T }

func (d ownDockerTripwire) Run(_ context.Context, host string, req docker.Command) ([]byte, error) {
	if host == "" {
		d.t.Errorf("AppTown's own Docker was asked: docker %s", strings.Join(req.Args, " "))
	}
	return nil, errors.New("no daemon in this test")
}

// modeApp is an AppTown started with -no-local-docker on db and dataDir ("" a
// new one). The mode needs a worker configured; tests that run tasks send
// them to an in-process worker (onWorker).
func modeApp(t *testing.T, db *database.DB, dataDir string) *App {
	t.Helper()
	return newApp(t, db, Config{DataDir: dataDir, NoLocalDocker: true, ConsoleCLI: fixtureCLI(t), DockerClient: ownDockerTripwire{t},
		Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")})
}

// The mode starts only with a worker and an external database: the built-in
// database is a container on AppTown's own Docker.
func TestTheNoLocalDockerModeNeedsAWorkerAndAnExternalDatabase(t *testing.T) {
	t.Parallel()
	worker := workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")
	for _, c := range []struct {
		cfg Config
		ok  bool
	}{
		{Config{NoLocalDocker: true}, false},
		{Config{NoLocalDocker: true, Worker: worker, UsingBuiltinDB: true}, false},
		{Config{NoLocalDocker: true, Worker: worker}, true},
	} {
		if err := c.cfg.Validate(); (err == nil) != c.ok || err != nil && !strings.Contains(err.Error(), "-worker-url") {
			t.Fatalf("worker %v, built-in database %v: %v", c.cfg.Worker != nil, c.cfg.UsingBuiltinDB, err)
		}
	}
}

// In the mode AppTown's own Docker and its project-process runner refuse,
// before any process starts, while its own Git still runs and an attached
// target's scope reaches the Docker client with that target's host.
func TestTheNoLocalDockerModeRefusesAppTownsOwnDockerAndProjectProcesses(t *testing.T) {
	t.Parallel()
	a := modeApp(t, testDB(t), "")
	if _, err := a.docker.Run(context.Background(), "ps"); !errors.Is(err, docker.ErrNoLocalBackend) {
		t.Fatalf("AppTown's own Docker: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	for _, backend := range []string{execution.Local, execution.Docker} {
		out := projectProcesses(a).Run(context.Background(), backend, execution.Request{Argv: []string{"touch", marker}, Image: "alpine", Name: "forge-x", Timeout: time.Minute})
		if !errors.Is(out.RunErr, docker.ErrNoLocalBackend) {
			t.Fatalf("a project process (%s): %+v", backend, out)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a project process ran on AppTown: %v", err)
	}
	if out := a.processRunner.Run(context.Background(), execution.Local, execution.Request{Argv: []string{"git", "--version"}, Timeout: time.Minute}); !out.Exited0() {
		t.Fatalf("AppTown's own Git: %+v", out)
	}
	if _, err := a.docker.WithHost("ssh://apptown-target-lab", "lab", "").Run(context.Background(), "info"); err == nil || errors.Is(err, docker.ErrNoLocalBackend) {
		t.Fatalf("an attached target's call did not reach its transport: %v", err)
	}
}

// A database no run was ever recorded in starts in the mode.
func TestAFreshDatabaseStartsInTheNoLocalDockerMode(t *testing.T) {
	t.Parallel()
	if err := modeApp(t, testDB(t), "").RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a fresh database in the mode: %v", err)
	}
}

// A restart in the mode after a start in it is not a cutover: nothing local
// can have run since, so a step AppTown left running (here one that runs
// in AppTown itself, with no task) does not stop the start, and startup
// repair settles it as on any restart.
func TestARestartInTheNoLocalDockerModeIsNotACutover(t *testing.T) {
	t.Parallel()
	a := modeApp(t, testDB(t), "")
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("the first start in the mode: %v", err)
	}
	_, stepID := admittedRunnerStep(t, a, "deploying", "deploy", "deploy", false)
	if step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID); err != nil || step.Status != workflow.StatusRunning {
		t.Fatalf("the claimed step: %+v %v", step, err)
	}
	b := modeApp(t, a.DB, a.cfg.dataDir)
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a restart in the mode: %v", err)
	}
	if step, err := workflow.ReadStepIn(context.Background(), b.DB, stepID); err != nil || step.Status != workflow.StatusError {
		t.Fatalf("startup repair of the step: %+v %v", step, err)
	}
}

// A chat AppTown ran, ended before the switch, stays ended once AppTown starts
// in the mode: it cannot be woken, its record stays, and its change is handed
// to a workflow without asking Docker who writes its folder.
func TestALocalChatIsEndedBeforeTheNoLocalDockerModeAndHandsItsChangeOn(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("the chat: %d %s", code, body)
	}
	if err := w.a.changeConsole.End(context.Background(), id, "owner"); err != nil {
		t.Fatal(err)
	}
	b := modeApp(t, w.a.DB, w.a.cfg.dataDir)
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a start once the chat ended: %v", err)
	}
	if err := b.RecoverConsoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, attachErr := b.Console.Attach(context.Background(), id, console.Size{})
	for what, err := range map[string]error{"warm": b.Console.Warm(context.Background(), id), "send": b.Console.Send(context.Background(), id, "hello"), "attach": attachErr} {
		if err == nil {
			t.Fatalf("%s woke the old local chat", what)
		}
	}
	if meta, err := b.Console.Recorded(id); err != nil || meta.ID != id {
		t.Fatalf("the old chat's record: %+v %v", meta, err)
	}
	// The next holder (a chat on the worker) takes the change through the
	// holder rule, with this AppTown's presence.
	next := scm.ChatHolder(b.Console.NewSessionID())
	if err := b.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TakeChangeIn(context.Background(), tx, w.change.ProjectID, w.change.Branch, next, holderPresenceFor(b), time.Now())
		return err
	}); err != nil {
		t.Fatalf("the handoff in the mode: %v", err)
	}
	if authority, err := scm.ReadAuthorityIn(context.Background(), b.DB, w.change.ProjectID, w.change.Branch); err != nil || authority.Holder != next {
		t.Fatalf("the change's holder: %+v %v", authority, err)
	}
}

// A script in the mode runs on the worker: the check that nothing local still
// writes its folder is answered by the mode, not by asking Docker.
func TestAScriptRunsOnTheWorkerInTheNoLocalDockerMode(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixtureIn(t, modeApp(t, testDB(t), ""), map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0"}})
	onWorker(t, a, fake)
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
		t.Fatal(err)
	}
	if settled := awaitStep(t, a, step.ID, workflow.StatusPass); settled.Status != workflow.StatusPass {
		t.Fatalf("the script: %s", settled.Evidence)
	}
}

// Interrupting an agent step whose attempt runs as a task records the stop
// and has the task's backend (here a worker) confirm it, on any AppTown; a
// stop the backend does not confirm is reported, and stays recorded.
func TestInterruptingAStepStopsItsTaskConfirmed(t *testing.T) {
	t.Parallel()
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprintf("confirmed=%v", confirmed), func(t *testing.T) {
			t.Parallel()
			daemon := newFakeDocker()
			a := agentTestApp(t, Config{DockerClient: daemon})
			testAdministrator(t, a, "bls")
			_, stepID := admittedRunnerStep(t, a, "irt", "agent", "agent/pi", false)
			step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
			if err != nil {
				t.Fatal(err)
			}
			fake := &executionfake.Tasks{}
			onWorker(t, a, fake)
			op := fmt.Sprintf("step-%d-attempt-%d-agent", stepID, step.Attempt)
			spec := execution.TaskSpec{Key: op, Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault}, Image: "fake", Argv: []string{"pi"}, Env: []string{"FAKE_BLOCK=1"}}
			obs, err := a.tasks.Driver.Ensure(context.Background(), spec)
			if err == nil {
				err = a.tasks.Driver.Start(context.Background(), execution.TaskRef{Key: op, ProviderID: obs.ID})
			}
			if err != nil {
				t.Fatal(err)
			}
			run, _ := workflow.ReadRunIn(context.Background(), a.DB, step.RunID)
			if _, err := a.tasks.Records.Reserve(context.Background(), tasks.Admission{Operation: op, Owner: tasks.Owner{ProjectID: run.ProjectID, RunID: run.ID, StepID: stepID, Attempt: step.Attempt},
				Invocation: "agent", Backend: a.tasks.Backend, Container: "apptown-task-" + op, Digest: spec.Digest(), Admitted: spec.Admission, Deadline: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := a.tasks.Records.RecordProvider(context.Background(), op, obs.ID); err != nil {
				t.Fatal(err)
			}
			if !confirmed {
				a.tasks.Driver = unconfirmedStops{a.tasks.Driver}
			}
			req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/steps/%d/interrupt", stepID), strings.NewReader("{}"))
			req.Header.Set("X-AppTown-Actor", "bls")
			resp := httptest.NewRecorder()
			asked := len(daemon.joined())
			a.Handler().ServeHTTP(resp, req)
			if len(daemon.joined()) != asked {
				t.Fatalf("a step running as a task was stopped through Docker: %v", daemon.joined()[asked:])
			}
			attempt, err := a.tasks.Records.Get(context.Background(), op)
			if err != nil || !attempt.StopWanted {
				t.Fatalf("the stop is not recorded: %+v %v", attempt, err)
			}
			running := fake.Present(op)
			if obs, err := fake.Observe(context.Background(), execution.TaskRef{Key: op}); err == nil {
				running = obs.State == execution.TaskRunning
			}
			switch {
			case confirmed && (resp.Code != 200 || !strings.Contains(resp.Body.String(), "apptown-task-"+op) || running):
				t.Fatalf("a confirmed stop: %d %s, still running %v", resp.Code, resp.Body.String(), running)
			case !confirmed && (resp.Code != 502 || !strings.Contains(resp.Body.String(), op) || !running):
				t.Fatalf("an unconfirmed stop: %d %s, still running %v", resp.Code, resp.Body.String(), running)
			}
		})
	}
}

// unconfirmedStops is a backend that does not answer a stop.
type unconfirmedStops struct{ execution.TaskDriver }

func (unconfirmedStops) Stop(context.Context, execution.TaskRef) error {
	return execution.ErrWorkerUnavailable
}
