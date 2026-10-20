package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// noHostWorld is a change whose working lane runs a script step (a
// recorded task, its project bound to a Node.js mirror) and then a tests
// step with command, on an AppTown composed with one worker that runs both
// tasks and checks, over its mutual TLS. This machine's Docker records
// every call and refuses any that is not a container listing.
type noHostWorld struct {
	a            *App
	fake         *executionfake.Tasks
	run          *workflow.Run
	script, test *workflow.Step
	calls, state string
}

func newNoHostWorld(t *testing.T, command string, scriptMinutes int) noHostWorld {
	t.Helper()
	a := testApp(t)
	fake := &executionfake.Tasks{}
	a.tasks.Poll = 20 * time.Millisecond
	pid := plainTestProject(t, a.DB, "no-host", "No Host")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "proof", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working",
		Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
			{Name: "make", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake",
				"timeoutMinutes": scriptMinutes, "env": map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0", "FAKE_BLOCK": "1"}}},
			{Name: "tests", Uses: "tests", With: map[string]any{"command": command}},
		}}}}})
	if err := a.lifecycle.SelectLane(t.Context(), pid, "proof", true, "proof"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID)), "folder")
	bindNodeMirror(t, a, "https://mirror.invalid/node/")

	w := noHostWorld{a: a, fake: fake}
	dir := t.TempDir()
	w.calls = filepath.Join(dir, "calls")
	local := filepath.Join(dir, "local-docker")
	if err := execfile.Write(local, []byte("#!/bin/sh\necho \"local $@\" >> "+w.calls+"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ca := workertest.NewCA(t)
	w.state = t.TempDir()
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: w.state, Tasks: fake,
		Checks: execution.Runner{DockerBinary: workerCheckDocker(t, w.calls)}})
	a.cfg.worker = &execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.cfg.agentContext = "../../deploy/apptown-agent"
	a.tasks.Driver, a.tasks.Remote = *a.cfg.worker, *a.cfg.worker
	a.tasks.Profile.Name, a.tasks.Backend = "worker", "worker at "+srv.URL
	a.processRunner = execution.Runner{DockerBinary: local}
	refused := dockerfake.New()
	refused.SetReply(func(args []string) ([]byte, error, bool) {
		f, _ := os.OpenFile(w.calls, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		f.WriteString("local " + strings.Join(args, " ") + "\n")
		f.Close()
		if len(args) > 0 && args[0] == "ps" {
			return nil, nil, true
		}
		return nil, os.ErrPermission, true
	})
	a.docker.Client = refused
	rebuildWork(t, a, func(*workflow.Dependencies) {}) // composed from the configuration, as startup does

	a.Work.Stop()
	run, err := a.Work.StartRun(a.asForge(context.Background(), ch.ProjectID, "proof"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "proof"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(a.asForge(context.Background(), ch.ProjectID, "proof"), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("the proof's steps: %+v %v", snapshot, err)
	}
	w.run, w.script, w.test = run, snapshot.Steps[0], snapshot.Steps[1]
	return w
}

// unreachable points AppTown at a worker that does not answer, for both
// tasks and checks.
func (w noHostWorld) unreachable(t *testing.T) {
	t.Helper()
	w.a.cfg.worker.URL = "https://127.0.0.1:1"
	w.a.tasks.Driver, w.a.tasks.Remote = *w.a.cfg.worker, *w.a.cfg.worker
	rebuildWork(t, w.a, func(*workflow.Dependencies) {})
}

// runScript runs the script step on the worker, reads the generated mise
// configuration the worker keeps in the task's own folder while it runs,
// and lets the task end.
func (w noHostWorld) runScript(t *testing.T) (config string) {
	t.Helper()
	executed := make(chan error, 1)
	go func() { executed <- w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), w.script.ID) }()
	key := taskKeyOf(w.script)
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return w.fake.Starts(key) > 0 }) {
		t.Fatal("the script's task never started on the worker")
	}
	files, _ := filepath.Glob(filepath.Join(w.state, "task-files", key, "*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "node.mirror_url") {
			config = string(b)
		}
	}
	w.fake.Exit(key, 0)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	awaitStep(t, w.a, w.script.ID, workflow.StatusPass)
	return config
}

// nothingRanHere says this machine's Docker was asked nothing but
// container listings.
func (w noHostWorld) nothingRanHere(t *testing.T) string {
	t.Helper()
	recorded, _ := os.ReadFile(w.calls)
	for _, line := range strings.Split(string(recorded), "\n") {
		if strings.HasPrefix(line, "local ") && !strings.HasPrefix(line, "local ps ") {
			t.Fatalf("a step touched this machine's Docker: %s\n%s", line, recorded)
		}
	}
	return string(recorded)
}

// With a worker configured, no script or tests step runs on this machine.
// An AppTown composed as startup composes it, whose own Docker refuses
// anything but a listing, runs a workflow with a script step bound to a
// registry and a tests step:
//   - the script runs as a task on the worker, which keeps its generated
//     configuration in the task's own folder;
//   - the tests step runs on the worker with the same configuration,
//     passing and failing by the worker's exit code, and a failure's base
//     rerun is a second worker run;
//   - a worker that cannot be reached is an error for both, never a pass
//     and never a run here (a script keeps asking until its deadline).
func TestNoScriptOrTestsStepRunsOnThisMachineWhenAWorkerIsConfigured(t *testing.T) {
	t.Parallel()
	t.Run("a script with a registry binding, then passing tests", func(t *testing.T) {
		t.Parallel()
		w := newNoHostWorld(t, "true", 10)
		if config := w.runScript(t); !strings.Contains(config, "https://mirror.invalid/node/") {
			t.Fatalf("the worker did not keep the script's mise configuration: %q", config)
		}
		step := reviewExecute(t, w.a, w.run, "tests")
		if step.Status != workflow.StatusPass {
			t.Fatalf("tests on the worker: %s %s", step.Status, step.Evidence)
		}
		recorded := w.nothingRanHere(t)
		if n := strings.Count(recorded, "--init --name forge-check-"); n != 1 {
			t.Fatalf("the worker ran %d tests checks, want 1:\n%s", n, recorded)
		}
		if n := strings.Count(recorded, "https://mirror.invalid/node/"); n != 1 {
			t.Fatalf("the tests check got the registry configuration %d times:\n%s", n, recorded)
		}
	})
	t.Run("failing tests, and their base rerun", func(t *testing.T) {
		t.Parallel()
		w := newNoHostWorld(t, "exit 7", 10)
		w.runScript(t)
		step := reviewExecute(t, w.a, w.run, "tests")
		// The same tests fail on the base: carried, not this change's.
		if _, carried := stepEvidence(t, step)["carried"]; step.Status != workflow.StatusPass || !carried {
			t.Fatalf("failing tests on the worker: %s %s", step.Status, step.Evidence)
		}
		recorded := w.nothingRanHere(t)
		if n := strings.Count(recorded, "--init --name forge-check-"); n != 2 {
			t.Fatalf("the worker ran %d tests checks, want the change's and the base's:\n%s", n, recorded)
		}
		if n := strings.Count(recorded, "https://mirror.invalid/node/"); n != 2 {
			t.Fatalf("%d of the 2 runs got the registry configuration:\n%s", n, recorded)
		}
	})
	t.Run("a worker that cannot be reached", func(t *testing.T) {
		t.Parallel()
		w := newNoHostWorld(t, "true", 1)
		w.unreachable(t)
		// A script task asks an unanswering worker again until its deadline,
		// here shortened to two seconds, and then is an error.
		w.a.tasks.ScriptWallClock = 2 * time.Second
		if err := w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), w.script.ID); err != nil {
			t.Fatal(err)
		}
		if script := awaitStep(t, w.a, w.script.ID, workflow.StatusError); script.Status != workflow.StatusError {
			t.Fatalf("a script whose worker cannot be reached: %s %s", script.Status, script.Evidence)
		}
		if step := reviewExecute(t, w.a, w.run, "tests"); step.Status != workflow.StatusError {
			t.Fatalf("tests whose worker cannot be reached: %s %s", step.Status, step.Evidence)
		}
		if w.fake.Starts(taskKeyOf(w.script)) != 0 {
			t.Fatal("a task started although the worker could not be reached")
		}
		w.nothingRanHere(t)
	})
}
