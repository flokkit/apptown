package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// taskAppTown is one AppTown process's part in running a script step as a
// recorded task: Work, the script runner over the task runner, and the
// branch owner, over one database and data directory, assembled as the
// daemon assembles them. Its tasks run on the execution fake (or a worker
// in this process serving it). A restart is another taskAppTown on the same
// database and directory. The composition root keeps the test that its own
// startup does the same (TestARestartNeverStartsARecordedTaskAgain).
type taskAppTown struct {
	// Historical-request tests admit a legacy plan through the real task owner,
	// then restart with the current cache scope; production has no override.
	scriptRunner workflow.Runner
	cacheProject ids.ID
	db           *database.DB
	dir          string
	image        string
	work         *workflow.Service
	tasks        *TaskRunner
	branches     *scm.Branches
	// ctx is the administrator the fixture acts as: it runs steps and reads
	// runs, as AppTown's engine does for itself.
	ctx         context.Context
	definitions map[string]schema.Spec
	// checks runs a tests step's command, as AppTown's check executor
	// would; a test that runs a tests step sets it.
	checks func(execution.Request) execution.Outcome
	// agents runs agent and review steps on this machine, their Pi run by
	// pi; a test that runs one sets it (installFakePi).
	agents *Executor
	pi     *fakePi
	// projectID is the fixture's one project: adopted, with its repository,
	// hub, on main.
	projectID ids.ID
	hub       string
	// executed is closed when the step startOnWorker runs has returned:
	// after it, that execution acts no more.
	executed chan struct{}
}

// noPublisher is a project with no provider remote: nothing is mirrored.
type noPublisher struct{}

func (noPublisher) PushBranch(context.Context, ids.ID, string, string) {}

// noEffects is an AppTown whose abandonments and completions start no
// further work: no job queue takes part in this package's tests.
type noEffects struct{}

func (noEffects) RecordAbandonIn(context.Context, *database.Tx, workflow.AbandonEffects) error {
	return nil
}
func (noEffects) RecordCompletionIn(context.Context, *database.Tx, workflow.CompletedRun) (workflow.ReleaseReceipt, error) {
	return workflow.ReleaseReceipt{}, nil
}
func (noEffects) RetryCompletionIn(context.Context, *database.Tx, ids.ID) (bool, error) {
	return false, nil
}

// noLaws is a project that adopted no policy: no workflow breaks one.
type noLaws struct{}

func (noLaws) ViolationsIn(context.Context, database.Queryer, ids.ID, *schema.Spec) ([]workflow.LawViolation, error) {
	return nil, nil
}

// taskAppTownAdministrator is the person every taskAppTown acts as.
const taskAppTownAdministrator = "fixture"

// startTaskAppTown builds an AppTown process on db and dir whose script steps
// run their tasks on the local profile, on the fake driver until a test
// names another, with image as the default image. definitions are the
// workflows its runs are admitted under.
func startTaskAppTown(t *testing.T, db *database.DB, dir, image string, definitions map[string]schema.Spec) *taskAppTown {
	t.Helper()
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: taskAppTownAdministrator, Via: "session"}, Verified: true})
	f := &taskAppTown{db: db, dir: dir, image: image, ctx: ctx, definitions: definitions}
	locks := &scm.RepositoryLocks{}
	f.branches = scm.NewBranches(db, locks)
	f.tasks = &TaskRunner{Records: tasks.New(db), Driver: &executionfake.Tasks{}, Requests: filepath.Join(dir, "tasks"),
		Profile: TaskProfile{Name: "local-docker", Network: execution.NetworkDefault}, Backend: "local-docker fixture on the fake"}
	commands := NewCommands(db, CommandDependencies{
		Runner: execution.Runner{Scratch: filepath.Join(dir, "generated")}, Progress: &progress.Feeds{}, Publisher: noPublisher{},
		Branches: f.branches, Tasks: f.tasks, Docker: &docker.Scope{Client: dockerfake.New()}, Catalog: dependency.NewCatalog(db, nil),
		// The check executor runs a tests step's command: here the test's,
		// through f.checks.
		Checks: CompileCheck{Run: func(_ context.Context, req execution.Request) execution.Outcome {
			if f.checks == nil {
				t.Errorf("a tests step ran a check this test did not expect: %v", req.Argv)
				return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("no check executor in this test")}
			}
			return f.checks(req)
		}, Image: "apptown-agent:test"},
	}, CommandConfig{DataDir: dir, Image: image})
	f.agents = f.agentExecutor(t)
	f.work = workflow.New(context.Background(), db, workflow.Dependencies{
		Authority: access.Checked{Authority: directory, Projects: project.New(db, directory)},
		Resolve: func(_ context.Context, req workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			spec, ok := f.definitions[req.WorkflowName]
			if !ok {
				return workflow.PreparedRun{}, fmt.Errorf("the fixture has no workflow %q", req.WorkflowName)
			}
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: req.WorkflowName, Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
		StepContext: func(ctx context.Context, _ ids.ID) context.Context {
			if f.cacheProject.IsZero() {
				return ctx
			}
			return agentenv.WithCacheScope(ctx, f.cacheProject)
		},
		Laws: noLaws{},
		Merge: workcommands.LandingEffects{DB: db, Contracts: project.New(db, nil).Contracts(), DataDir: dir,
			Drivers: scm.GitDrivers{Runner: execution.Runner{Scratch: filepath.Join(dir, "generated")}, DataDir: dir}, Publisher: noPublisher{}},
		Abandonment: noEffects{},
		Completion:  noEffects{},
		Runners: map[string]workflow.Runner{ScriptKind: func(task *workflow.Execution) {
			if f.scriptRunner != nil {
				f.scriptRunner(task)
				return
			}
			commands.RunScript(task)
		}, TestsKind: commands.RunTests,
			AgentKind: f.agents.Run, ReviewKind: f.agents.Review},
		Recoverers:   map[string]workflow.Runner{ScriptKind: commands.RecoverScript, AgentKind: f.agents.RecoverAgent, ReviewKind: f.agents.RecoverAgent},
		Git:          workflow.GitWorkspace{DataDir: dir},
		Repositories: locks,
		Automatic:    []string{ScriptKind, TestsKind, AgentKind, ReviewKind},
		DataDir:      dir,
		// Work notices a run abandoned under a running step within a
		// fraction of a second: quick for the tests that abandon one, and
		// light enough (two reads per running step) for the package's
		// parallel tests sharing one database.
		AttemptWatch: 200 * time.Millisecond,
	})
	t.Cleanup(func() {
		f.work.Stop()
		f.work.Wait()
	})
	return f
}

// fakePi stands in for Pi: it records every request and answers each with
// whatever the test scripts for that call, so an agent or review step runs
// its outcome logic without Docker or a model.
type fakePi struct {
	mu    sync.Mutex
	calls []execution.Request
	on    func(n int, req execution.Request) execution.Outcome // n is 1-based
}

func (p *fakePi) Run(_ context.Context, _ string, req execution.Request) execution.Outcome {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	n, on := len(p.calls), p.on
	p.mu.Unlock()
	return piStandIn(req, func(req execution.Request) execution.Outcome { return on(n, req) })
}

func (p *fakePi) requests() []execution.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]execution.Request(nil), p.calls...)
}

// apptownPi is the Pi an AppTown process runs: the test's, once it installs one.
type apptownPi struct{ f *taskAppTown }

func (p apptownPi) Run(_ context.Context, name string, req execution.Request) execution.Outcome {
	if p.f.pi == nil {
		return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("no Pi in this test")}
	}
	return p.f.pi.Run(context.Background(), name, req)
}

// installFakePi makes on answer every Pi invocation of f's agent and review
// steps.
func installFakePi(f *taskAppTown, on func(n int, req execution.Request) execution.Outcome) *fakePi {
	f.pi = &fakePi{on: on}
	return f.pi
}

// agentExecutor is the agent runner the daemon assembles for this
// machine's Docker, over the owners whose records an
// agent step reads, with Pi run by the test's fake.
func (f *taskAppTown) agentExecutor(t *testing.T) *Executor {
	t.Helper()
	tokens := &identity.RuntimeTokens{}
	if err := tokens.Open(filepath.Join(f.dir, identity.RuntimeTokenKeyFile)); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(f.dir, "apptown-linux")
	if err := execfile.Write(cli, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	scope := &docker.Scope{Client: dockerfake.New()}
	feeds := &progress.Feeds{}
	secrets := secret.NewService(f.db, secret.Config{DataDir: f.dir})
	integrations := integration.NewService(f.db, integration.Dependencies{Projects: project.New(f.db, nil), Secrets: secrets})
	adoptions := adoption.New(f.db, adoption.Dependencies{Ways: agentCatalog{}})
	models := selectionWithDefault(t, f.db, "fake/model")
	return New(f.db, Dependencies{
		Adoption: adoptions, Integrations: integrations, Secrets: secrets, Catalog: dependency.NewCatalog(f.db, nil),
		Models: models, Capacity: model.NewScheduler(nil, 1), Progress: feeds, Tokens: tokens, Docker: scope,
		Workshop: agentenv.NewWorkshop(scope, integrations, feeds, f.dir, f.image, stock.Images().PrivateDocker, nil), Instructions: agentenv.NewInstructions(f.db, integrations),
		Home: piharness.NewPreparer(gatewayModels{}, models, adoptions, scope, f.image), CLI: fileCLI(cli),
		Process: apptownPi{f}, Ledger: usage.New(f.db), Publisher: noPublisher{}, Branches: f.branches,
	}, Config{DataDir: f.dir, Image: f.image, PrivateDocker: true}) // apptownd's default
}

// fakeTaskFixture admits one script step whose task runs on the fake driver.
// block makes the task run until the test ends it.
func fakeTaskFixture(t *testing.T, block bool) (*taskAppTown, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	env := map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0"}
	if block {
		env["FAKE_BLOCK"] = "1"
	}
	return scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": env})
}

// scriptTaskFixture admits one script step configured by with on a
// repository-backed change of a new project: the change is begun and its
// folder prepared, and the run admitted with dispatch stopped, so the test
// runs the step.
func scriptTaskFixture(t *testing.T, with map[string]any) (*taskAppTown, *executionfake.Tasks, *workflow.Step) {
	t.Helper()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "build", Steps: []schema.Step{{Name: "make", Uses: ScriptKind, With: with}}},
	}}
	f, fake := newTaskAppTown(t, "work", spec)
	run := f.startChangeRun(t, "work", f.ctx)
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("script steps: %+v %v", snapshot, err)
	}
	return f, fake, snapshot.Steps[0]
}

// taskHubTemplate is the new project repository every task fixture's hub
// starts as, made once by AppTown's own initializer (its identity, ignore
// file, first commit and no-push hook) and copied for each fixture.
var taskHubTemplate = gittest.NewTemplate(func(t testing.TB, dir string) {
	if err := scm.InitializeRepository(context.Background(), dir, "main"); err != nil {
		t.Fatal(err)
	}
})

// newTaskAppTown is an AppTown on a fresh database and directory with one
// project, adopted and with a repository on main, whose runs are admitted
// under the named workflow. Its tasks run on the returned fake.
func newTaskAppTown(t *testing.T, name string, spec schema.Spec) (*taskAppTown, *executionfake.Tasks) {
	t.Helper()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	if err := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)}).SeedAdministrator(context.Background(), taskAppTownAdministrator); err != nil {
		t.Fatal(err)
	}
	f := startTaskAppTown(t, db, dir, "apptown-agent", map[string]schema.Spec{name: spec})
	const slug = "fake-tasks"
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: "Fake Tasks"})
		if err != nil {
			return ids.Nil, err
		}
		if err := adoption.InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return ids.Nil, err
		}
		return id, adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "task fixture", adoption.EmptyReceipt(), taskAppTownAdministrator, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	f.hub = filepath.Join(dir, "workspaces", "project-"+projectID.String())
	taskHubTemplate.CopyTo(t, f.hub)
	if err := project.New(db, nil).SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	f.projectID = projectID
	fake := &executionfake.Tasks{}
	f.tasks.Driver, f.tasks.Poll = fake, 20*time.Millisecond
	return f, fake
}

// startChangeRun begins a change of the fixture's project and admits a run
// of the named workflow for it as starter.
func (f *taskAppTown) startChangeRun(t *testing.T, name string, starter context.Context) *workflow.Run {
	t.Helper()
	return f.admit(t, f.beginChange(t), name, starter)
}

// beginChange begins a change of the fixture's project and prepares its
// folder.
func (f *taskAppTown) beginChange(t *testing.T) *workflow.Change {
	t.Helper()
	ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Type: "feature", Title: "fake tasks"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	return ch
}

// admit stops dispatch and admits a run of the named workflow for ch as
// starter, so the test runs its steps.
func (f *taskAppTown) admit(t *testing.T, ch *workflow.Change, name string, starter context.Context) *workflow.Run {
	t.Helper()
	f.work.Stop()
	run, err := f.work.StartRun(starter, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: name})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run
}

func taskKeyOf(step *workflow.Step) string { return fmt.Sprintf("step-%d-attempt-1", step.ID) }

// taskBackends are where the recovery guarantees are checked: the local
// driver, and a worker reached over the wire.
var taskBackends = []string{"local", "worker"}

// onWorker moves AppTown's tasks to a worker served in this process over
// mutual TLS, with the fake behind it, as an installation configured with a
// worker does. It returns the worker's state directory.
func (f *taskAppTown) onWorker(t *testing.T, fake *executionfake.Tasks) string {
	t.Helper()
	ca, state := workertest.NewCA(t), t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, fake))
	f.tasks.Driver, f.tasks.Remote, f.tasks.Profile.Name, f.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	return state
}

// recover is the next process's startup for recorded tasks, as the
// composition root runs it: steps whose attempts recorded a task are kept
// running while every other interrupted step is ended, then recorded tasks
// are sorted and the steps still theirs resumed by the dispatcher.
func (f *taskAppTown) recover(t *testing.T) {
	t.Helper()
	if _, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (workflow.InterruptedSteps, error) {
		withTasks, err := tasks.UnsettledStepsIn(f.ctx, tx)
		if err != nil {
			return workflow.InterruptedSteps{}, err
		}
		return workflow.InterruptStepsIn(f.ctx, tx, withTasks, []string{DeployKind}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	adopt, err := f.tasks.Resume(f.ctx, f.work)
	if err != nil {
		t.Logf("resuming recorded tasks: %v", err)
	}
	f.work.ResumePending(adopt...)
}

// restarted is AppTown after a restart: a new process on f's database
// and data directory, its tasks on the same backend, having resumed
// recorded work. f's dispatch must be stopped.
func restarted(t *testing.T, f *taskAppTown) *taskAppTown {
	t.Helper()
	g := startTaskAppTown(t, f.db, f.dir, f.image, f.definitions)
	g.projectID, g.hub = f.projectID, f.hub
	g.tasks.Driver, g.tasks.Remote, g.tasks.Profile, g.tasks.Backend, g.tasks.Poll = f.tasks.Driver, f.tasks.Remote, f.tasks.Profile, f.tasks.Backend, f.tasks.Poll
	g.recover(t)
	return g
}

// startOnWorker runs the fixture's step until its task is running on the
// worker, and returns the run and the task's workspace there.
func startOnWorker(t *testing.T, configure ...func(*taskAppTown, *workflow.Step)) (*taskAppTown, *executionfake.Tasks, *workflow.Step, *workflow.Run, string) {
	t.Helper()
	f, fake, step := fakeTaskFixture(t, true)
	for _, before := range configure {
		before(f, step)
	}
	state := f.onWorker(t, fake)
	run, err := workflow.ReadRunIn(context.Background(), f.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// ExecuteStep runs the script to its end, which waits for the task the
	// test ends.
	executed := make(chan error, 1)
	f.executed = make(chan struct{})
	go func() {
		err := f.work.ExecuteStep(f.ctx, step.ID)
		close(f.executed)
		executed <- err
	}()
	key := taskKeyOf(step)
	t.Cleanup(func() {
		fake.Exit(key, 137) // a test that stopped early leaves nothing waiting
		if err := <-executed; err != nil {
			t.Error(err)
		}
	})
	awaitStarted(t, fake, key, "the task never started on the worker")
	return f, fake, step, run, filepath.Join(state, "workspaces", key)
}

// awaitStarted waits for the fake to start the task under key.
func awaitStarted(t *testing.T, fake *executionfake.Tasks, key, failure string) {
	t.Helper()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return fake.Starts(key) > 0 }) {
		t.Fatal(failure)
	}
}

// drained waits for everything AppTown's Work has dispatched to finish:
// after it, no dispatched step is still going to act. It fails the test
// rather than hang.
func drained(t *testing.T, f *taskAppTown) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("AppTown's dispatched work did not finish")
	}
}

// awaitStep waits for the step to reach want, reading it as Work records it.
func awaitStep(t *testing.T, f *taskAppTown, id ids.ID, want workflow.StepState) *workflow.Step {
	t.Helper()
	var step *workflow.Step
	var err error
	if !poll.Until(context.Background(), 90*time.Second, 10*time.Millisecond, func() bool {
		step, err = workflow.ReadStepIn(context.Background(), f.db, id)
		return err != nil || step.Status == want
	}) {
		t.Fatalf("step %d is %s, want %s: %s", id, step.Status, want, step.Evidence)
	}
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// source is the run's workflow source.
func (f *taskAppTown) source(t *testing.T, run *workflow.Run) workflow.Source {
	t.Helper()
	source, err := workflow.ReadSourceIn(context.Background(), f.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
