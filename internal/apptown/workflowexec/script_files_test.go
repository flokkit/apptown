package workflowexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
)

// bindNodeMirror binds a Node.js dist mirror: a registry that changes only
// the generated mise configuration, not the step's environment.
func bindNodeMirror(t *testing.T, a *taskAppTown, url string) {
	t.Helper()
	if _, err := integration.NewWithClock(a.db, nil).Upsert(t.Context(), "node-mirror", "registry", "registry", `{"format":"node-dist","url":"`+url+`"}`, false); err != nil {
		t.Fatal(err)
	}
}

// A script with registry bindings runs on the worker: its generated
// configuration travels with the task and the worker keeps it in the
// task's own folder while it runs.
func TestAScriptWithRegistryBindingsRunsOnTheWorker(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	bindNodeMirror(t, a, "https://mirror.invalid/node/")
	state := a.onWorker(t, fake)
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started on the worker")
	files, _ := filepath.Glob(filepath.Join(state, "task-files", key, "*"))
	var config string
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "node.mirror_url") {
			config = string(b)
		}
	}
	if !strings.Contains(config, "https://mirror.invalid/node/") {
		t.Fatalf("the worker does not keep the task's mise configuration: %q in %v", config, files)
	}
	fake.Exit(key, 0)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if files, _ := filepath.Glob(filepath.Join(state, "task-files", key, "*")); len(files) != 0 {
		t.Fatalf("a released task's files are kept: %v", files)
	}
}

// An AppTown that restarts recovers a recorded script task with the request it
// was admitted with, generated configuration included, whether or not the
// registry bindings changed while it was down: the request sent again carries the admitted mirror, never the
// current one, and the task runs once.
func TestARecoveredScriptTaskFollowsItsGeneratedFiles(t *testing.T) {
	t.Parallel()
	for _, backend := range taskBackends {
		for _, changed := range []bool{false, true} {
			name := backend + "/unchanged"
			if changed {
				name = backend + "/changed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				a, fake, step := fakeTaskFixture(t, true)
				bindNodeMirror(t, a, "https://mirror.invalid/node/")
				if backend == "worker" {
					a.onWorker(t, fake)
				}
				driver := &recordingDriver{TaskDriver: a.tasks.Driver}
				a.tasks.Driver, a.tasks.AbandonAt = driver, func(s string) bool { return s == "created" }
				if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
					t.Fatal(err)
				}
				a.tasks.AbandonAt = nil
				if changed {
					bindNodeMirror(t, a, "https://other-mirror.invalid/node/")
				}
				b := restarted(t, a)
				key := taskKeyOf(step)
				awaitStarted(t, fake, key, "the recovered task never started")
				fake.Exit(key, 0)
				awaitStep(t, b, step.ID, workflow.StatusPass)
				sent, _, _ := driver.record()
				if len(sent) != 2 || fake.Starts(key) != 1 {
					t.Fatalf("ensured %d times, started %d times", len(sent), fake.Starts(key))
				}
				var recovered execution.TaskSpec
				if err := json.Unmarshal(sent[1], &recovered); err != nil {
					t.Fatal(err)
				}
				var config string
				for _, f := range recovered.Files {
					config += string(f.Content)
				}
				if !strings.Contains(config, "https://mirror.invalid/node/") || strings.Contains(config, "other-mirror") {
					t.Fatalf("the recovered request's configuration: %q", config)
				}
				// The step passes as the task settles; the settlement's record
				// follows it, so it is awaited.
				var attempt tasks.Attempt
				var err error
				poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
					attempt, err = tasks.New(b.db).Get(context.Background(), key)
					return err == nil && attempt.Observed == tasks.Settled
				})
				if err != nil || attempt.Observed != tasks.Settled {
					t.Fatalf("the task's settlement was not recorded: %+v %v", attempt, err)
				}
			})
		}
	}
}

// A recorded script task killed at its memory limit is no answer, whatever
// code it exited with (a killed child in a pipeline can leave 0): the step
// is an error that names the limit, never a pass. An ordinary zero exit
// still passes.
//
// Kills: collection that read only the exit code.
func TestAScriptTaskKilledForMemoryIsAnError(t *testing.T) {
	t.Parallel()
	for _, oom := range []bool{true, false} {
		a, fake, step := fakeTaskFixture(t, true)
		executed := make(chan error, 1)
		go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
		key := taskKeyOf(step)
		awaitStarted(t, fake, key, "the task never started")
		if oom {
			fake.ExitOutOfMemory(key, 0)
		} else {
			fake.Exit(key, 0)
		}
		if err := <-executed; err != nil {
			t.Fatal(err)
		}
		if !oom {
			awaitStep(t, a, step.ID, workflow.StatusPass)
			continue
		}
		settled := awaitStep(t, a, step.ID, workflow.StatusError)
		if !strings.Contains(string(settled.Evidence), "ran out of memory") {
			t.Fatalf("the step's evidence: %s", settled.Evidence)
		}
	}
}
