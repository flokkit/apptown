package forge

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

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/maintenance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker/workertest"
)

// The no-local-Docker mode.

// ownDockerTripwire is the Docker client given to a forge in the mode. The
// mode wraps it (docker.Refusing), so only an attached target's call, which
// names its host, may reach it; a call without a host fails the test.
type ownDockerTripwire struct{ t *testing.T }

func (d ownDockerTripwire) Run(_ context.Context, host string, req docker.Command) ([]byte, error) {
	if host == "" {
		d.t.Errorf("AppTown's own Docker was asked: docker %s", strings.Join(req.Args, " "))
	}
	return nil, errors.New("no daemon in this test")
}

// modeApp is a forge started with -no-local-docker on db and dataDir ("" a
// new one). The mode needs a worker configured; tests that run tasks send
// them to an in-process worker (onWorker).
func modeApp(t *testing.T, db *database.DB, dataDir string) *App {
	t.Helper()
	return newApp(t, db, Config{DataDir: dataDir, NoLocalDocker: true, ConsoleCLI: fixtureCLI(t), DockerClient: ownDockerTripwire{t},
		Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")})
}

// drainApp is the forge with its own Docker on db and dataDir, run with
// -drain-local-docker. Its Docker lists containers (each a line as the drain
// asks docker ps for it: name, then its forge.class, forge.task, forge.step
// and forge.writes labels, tab-separated); it is asked nothing else.
func drainApp(t *testing.T, db *database.DB, dataDir string, containers ...string) *App {
	t.Helper()
	return newApp(t, db, Config{DataDir: dataDir, DrainLocalDocker: true, ConsoleCLI: fixtureCLI(t), DockerClient: dockerClientFunc(func(_ context.Context, host string, req docker.Command) ([]byte, error) {
		if host == "" && len(req.Args) > 0 && req.Args[0] == "ps" {
			return []byte(strings.Join(containers, "\n")), nil
		}
		t.Errorf("the drain asked Docker: %s", strings.Join(req.Args, " "))
		return nil, errors.New("not answered in this test")
	})})
}

// The mode starts only with a worker and an external database: the built-in
// database is a container on the forge's own Docker.
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

// In the mode the forge's own Docker and its project-process runner refuse,
// before any process starts, while its own Git still runs and an attached
// target's scope reaches the Docker client with that target's host.
func TestTheNoLocalDockerModeRefusesTheForgesOwnDockerAndProjectProcesses(t *testing.T) {
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
	if _, err := a.docker.WithHost("ssh://forge-target-lab", "lab", "").Run(context.Background(), "info"); err == nil || errors.Is(err, docker.ErrNoLocalBackend) {
		t.Fatalf("an attached target's call did not reach its transport: %v", err)
	}
}

// The drain into the mode is refused while the records hold local work,
// naming it, before Docker is asked; nothing is marked stopped or cleaned.
// Work on the worker alone does not stop it.
func TestTheDrainIntoTheNoLocalDockerModeIsRefusedWhileLocalWorkIsLeft(t *testing.T) {
	t.Parallel()
	reserve := func(t *testing.T, a *App, step *workflow.Step, backend string) string {
		t.Helper()
		run, err := workflow.ReadRunIn(context.Background(), a.DB, step.RunID)
		if err != nil {
			t.Fatal(err)
		}
		op := taskKeyOf(step)
		if _, err := a.tasks.Records.Reserve(context.Background(), tasks.Admission{Operation: op, Owner: tasks.Owner{ProjectID: run.ProjectID, RunID: run.ID, StepID: step.ID, Attempt: 1},
			Invocation: tasks.ScriptInvocation, Backend: backend, Container: "forge-task-" + op, Digest: "d",
			Admitted: execution.Admission{Profile: "fixture", Network: execution.NetworkDefault}, Deadline: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return op
	}
	start := func(t *testing.T, a *App) error {
		t.Helper()
		return drainApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context())
	}
	t.Run("a local task", func(t *testing.T) {
		t.Parallel()
		a, _, step := fakeTaskFixture(t, false)
		op := reserve(t, a, step, a.tasks.Backend)
		if err := start(t, a); err == nil || !strings.Contains(err.Error(), "task "+op) || !strings.Contains(err.Error(), "without -no-local-docker") {
			t.Fatalf("a drain with an unsettled local task: %v", err)
		}
		code := 0
		if err := a.tasks.Records.Settle(context.Background(), op, &code, "done"); err != nil {
			t.Fatal(err)
		}
		if err := start(t, a); err == nil || !strings.Contains(err.Error(), "task "+op) {
			t.Fatalf("a drain with a settled local task not released: %v", err)
		}
		if err := a.tasks.Records.ConfirmRelease(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if err := start(t, a); err != nil {
			t.Fatalf("a drain once the task is released: %v", err)
		}
	})
	t.Run("an outstanding workflow cleanup", func(t *testing.T) {
		t.Parallel()
		a := testApp(t)
		if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
			return jobQueue{}.EnqueueIn(context.Background(), tx, jobs.Request{Operation: workflowCompletionJob, Key: "1/cleanup", Values: map[string]any{"runId": 1, "effect": "cleanup"}})
		}); err != nil {
			t.Fatal(err)
		}
		if err := start(t, a); err == nil || !strings.Contains(err.Error(), workflowCompletionJob+" 1/cleanup") {
			t.Fatalf("a drain with an outstanding workflow cleanup: %v", err)
		}
	})
	t.Run("work on the worker only", func(t *testing.T) {
		t.Parallel()
		a, _, step := scriptTaskFixtureIn(t, testApp(t, Config{ConsoleAgent: consoletest.NewAgent(), ConsoleCLI: fixtureCLI(t), InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}), map[string]any{"run": "true"})
		reserve(t, a, step, maintenance.WorkerBackend+"https://worker.example")
		if _, err := a.Console.Open(context.Background(), console.OpenRequest{Scope: "change:1", Labels: map[string]string{conversation.PlacementLabel: conversation.OnWorker},
			Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls"}); err != nil {
			t.Fatal(err)
		}
		if live, err := a.Console.Unended(); err != nil || len(live) != 1 || !conversation.OnWorkerRecord(live[0]) {
			t.Fatalf("the chat on the worker: %+v %v", live, err)
		}
		if err := start(t, a); err != nil {
			t.Fatalf("a drain with work on the worker only: %v", err)
		}
	})
}

// A forge in the mode cannot see its own Docker, so a forge that last ran
// with it is not admitted to the mode until a drain has witnessed that
// Docker. An agent on a forge without a worker runs with no task attempt, so
// a crash leaves only its step recorded running and its container: startup
// repair ends the step, and the drain still refuses while Docker lists the
// container, naming it. Once Docker lists no execution container (a release
// container on the built-in target and the built-in database are not
// writers), the drain is recorded and the mode starts.
func TestTheDrainIntoTheNoLocalDockerModeWitnessesTheForgesOwnDocker(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, stepID := admittedRunnerStep(t, a, "cutover", "agent", "agent/pi", false)
	if held, err := a.tasks.Records.Held(context.Background()); err != nil || len(held) != 0 {
		t.Fatalf("a local agent has no task attempt: %+v %v", held, err)
	}
	if err := modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), "apptownd -drain-local-docker") {
		t.Fatalf("a start in the mode with no drain: %v", err)
	}
	if step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID); err != nil || step.Status != workflow.StatusRunning {
		t.Fatalf("the refused start changed the step: %+v %v", step, err)
	}
	agent := fmt.Sprintf("forge-step-%d-agent\tstep\t\t%d\t/data/runs/1", stepID, stepID)
	for _, left := range []string{agent, "forge-task-ns-op\t\top\t\t", "forge-script\t\t\t7\t/data/runs/1"} {
		name, _, _ := strings.Cut(left, "\t")
		if err := drainApp(t, a.DB, a.cfg.dataDir, left).RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("a drain with %s on AppTown's Docker: %v", name, err)
		}
		if err := modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err == nil {
			t.Fatalf("the mode started after a drain that saw %s", name)
		}
	}
	if step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID); err != nil || step.Status != workflow.StatusError {
		t.Fatalf("startup repair of the step: %+v %v", step, err)
	}
	if err := drainApp(t, a.DB, a.cfg.dataDir, "forge-env-game-main\tenv\t\t\t", "forge-db\tinfra\t\t\t", "someone-elses\t\t\t\t").RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a drain with no execution container left: %v", err)
	}
	if err := modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("the start in the mode after the drain: %v", err)
	}
}

// A Docker that cannot be asked witnesses nothing: no drain is recorded.
func TestTheDrainIsNotRecordedWhenTheForgesDockerCannotBeAsked(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	d := newApp(t, a.DB, Config{DataDir: a.cfg.dataDir, DrainLocalDocker: true, ConsoleCLI: fixtureCLI(t), DockerClient: dockerClientFunc(func(context.Context, string, docker.Command) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
	})})
	if err := d.RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), "could not be asked") {
		t.Fatalf("a drain with no Docker to ask: %v", err)
	}
	if err := modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err == nil {
		t.Fatal("the mode started after a drain that could not ask Docker")
	}
}

// A drain admits the switch into the mode, and restarts in it; a start with
// local Docker after it needs another drain. The drain itself needs the
// forge's own Docker, so it is refused in the mode.
func TestADrainIsNeededAgainAfterAStartWithLocalDocker(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	mode := func() error { return modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()) }
	if err := mode(); err == nil {
		t.Fatal("the mode started after a start with local Docker and no drain")
	}
	if err := drainApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := mode(); err != nil {
			t.Fatalf("a start in the mode after the drain: %v", err)
		}
	}
	if err := (Config{NoLocalDocker: true, DrainLocalDocker: true, Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")}).Validate(); err == nil || !strings.Contains(err.Error(), "without -no-local-docker") {
		t.Fatalf("a drain in the mode: %v", err)
	}
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mode(); err == nil || !strings.Contains(err.Error(), "apptownd -drain-local-docker") {
		t.Fatalf("the mode after a start with local Docker since the drain: %v", err)
	}
}

// A local startup that fails after its repair has committed (here, recording
// its Docker mode) has ended the step of a local agent without stopping it.
// The switch into the mode stays refused: only a drain, which asks the
// forge's own Docker, admits it.
func TestAFailedLocalStartupLeavesTheSwitchIntoTheModeRefused(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, stepID := admittedRunnerStep(t, a, "cutover", "agent", "agent/pi", false)
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO instance_settings")
	if err := a.RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), "recording the Docker mode") {
		t.Fatalf("expected the local startup to fail: %v", err)
	}
	if step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID); err != nil || step.Status != workflow.StatusError {
		t.Fatalf("repair state: %+v %v", step, err)
	}
	if err := modeApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), "apptownd -drain-local-docker") {
		t.Fatalf("the switch into the mode after a failed local startup: %v", err)
	}
}

// A database no run was ever recorded in starts in the mode with no drain:
// nothing local can have run.
func TestAFreshDatabaseStartsInTheNoLocalDockerMode(t *testing.T) {
	t.Parallel()
	if err := modeApp(t, testDB(t), "").RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a fresh database in the mode: %v", err)
	}
}

// A restart in the mode after a start in it is not a cutover: nothing local
// can have run since, so a step the forge left running (here one that runs
// in the forge itself, with no task) does not stop the start, and startup
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

// A deleted project's cleanup is outstanding until its owner records it
// done: while pending, and once its attempts are spent and it waits for a
// person, though its queued job has then completed.
func TestTheNoLocalDockerCutoverRefusesCleanupNeedingAttention(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	fx := fixtureWork(t, a)
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 1 && args[0] == "rm" && args[len(args)-1] == docker.PreviewChangeName(fx.Change) {
			return "permission denied", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	deleteProject(t, a, "game")
	purgeProject(t, a, "game")
	start := func() error { return drainApp(t, a.DB, a.cfg.dataDir).RecoverInterruptedWork(t.Context()) }
	if err := start(); err == nil || !strings.Contains(err.Error(), "cleanup of deleted project game ("+idstest.N(1).String()+"), pending") {
		t.Fatalf("a drain with a pending cleanup: %v", err)
	}
	for range projectlifecycle.CleanupAttempts {
		runCleanup(t, a, idstest.N(1), 0)
	}
	if d, err := a.Projects.Deletion(context.Background(), idstest.N(1)); err != nil || d.State != project.CleanupNeedsAttention {
		t.Fatalf("the spent cleanup: %+v %v", d, err)
	}
	if err := start(); err == nil || !strings.Contains(err.Error(), "cleanup of deleted project game ("+idstest.N(1).String()+"), needs-attention") {
		t.Fatalf("a drain with a cleanup needing attention: %v", err)
	}
}

// A chat the forge ran keeps the drain into the mode refused until it has
// ended. Then the drain is recorded and the forge starts in the mode; the chat cannot be woken, its record stays,
// and its change is handed to a workflow without asking Docker who writes
// its folder.
func TestALocalChatIsEndedBeforeTheNoLocalDockerModeAndHandsItsChangeOn(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("the chat: %d %s", code, body)
	}
	if err := drainApp(t, w.a.DB, w.a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), "chat "+id) {
		t.Fatalf("a drain with a live local chat: %v", err)
	}
	if err := w.a.changeConsole.End(context.Background(), id, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := drainApp(t, w.a.DB, w.a.cfg.dataDir).RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("a drain once the chat ended: %v", err)
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
	// holder rule, with this forge's presence.
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
// and has the task's backend (here a worker) confirm it, on any forge; a
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
				Invocation: "agent", Backend: a.tasks.Backend, Container: "forge-task-" + op, Digest: spec.Digest(), Admitted: spec.Admission, Deadline: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := a.tasks.Records.RecordProvider(context.Background(), op, obs.ID); err != nil {
				t.Fatal(err)
			}
			if !confirmed {
				a.tasks.Driver = unconfirmedStops{a.tasks.Driver}
			}
			req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/steps/%d/interrupt", stepID), strings.NewReader("{}"))
			req.Header.Set("X-Forge-Actor", "bls")
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
			case confirmed && (resp.Code != 200 || !strings.Contains(resp.Body.String(), "forge-task-"+op) || running):
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
