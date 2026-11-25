package workflowexec

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// leftoverSpec is a task of the kind AppTown never records (a model-judged
// check): its own workspace from source, admitted and ensured by key.
func leftoverSpec(key, repository, commit string) execution.TaskSpec {
	return execution.TaskSpec{Key: key, Image: "fake", Argv: []string{"run"}, Env: []string{"FAKE_BLOCK=1"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault, Expires: time.Now().Add(time.Hour)},
		Source:    &execution.TaskSource{Repository: repository, Commit: commit}}
}

// ensureOn admits and ensures spec through driver.
func ensureOn(t *testing.T, driver execution.WorkerTasks, spec execution.TaskSpec) {
	t.Helper()
	if _, admitted, err := driver.Admit(t.Context(), spec.Key); err != nil || !admitted {
		t.Fatalf("admitting %s: %v %v", spec.Key, admitted, err)
	}
	if _, err := driver.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensuring %s: %v", spec.Key, err)
	}
}

// admittedWithin asks for a slot for at most wait: a worker with none free
// answers only after its slice, so a wait that ends unanswered is "no".
func admittedWithin(t *testing.T, driver execution.WorkerTasks, key string, wait time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	_, admitted, err := driver.Admit(ctx, key)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		t.Fatalf("admitting %s: %v", key, err)
	}
	return err == nil && admitted
}

func holdings(t *testing.T, state string) (tasks, workspaces []string) {
	t.Helper()
	records, _ := os.ReadDir(filepath.Join(state, "records"))
	for _, e := range records {
		tasks = append(tasks, strings.TrimSuffix(e.Name(), ".json"))
	}
	dirs, _ := os.ReadDir(filepath.Join(state, "workspaces"))
	for _, e := range dirs {
		workspaces = append(workspaces, e.Name())
	}
	return tasks, workspaces
}

// The demo's deadlock: a one-slot worker restarted over a task of an
// AppTown process that stopped mid-check (no record names it, no process
// has it in hand) counts it as holding its slot. AppTown's reconcile
// releases it, record and workspace, and the next task is admitted.
func TestALeftoverAppTownNoLongerKnowsFreesTheWorkersOnlySlot(t *testing.T) {
	t.Parallel()
	ca, state, fake := workertest.NewCA(t), t.TempDir(), &executionfake.Tasks{}
	hub := filepath.Join(t.TempDir(), "hub")
	taskHubTemplate.CopyTo(t, hub)
	commit := scm.Ref(t.Context(), hub, "refs/heads/main")

	before := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Tasks: fake, TaskLimit: 1, StateDisk: workertest.Disk})
	ensureOn(t, workertest.Driver(t, ca, before), leftoverSpec("check-leftover", hub, commit))
	before.Close()

	after := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Tasks: fake, TaskLimit: 1, StateDisk: workertest.Disk})
	driver := workertest.Driver(t, ca, after)
	if admittedWithin(t, driver, "next", 300*time.Millisecond) {
		t.Fatal("the restarted worker admitted a task beside the leftover holding its only slot")
	}
	runner := &TaskRunner{Records: tasks.New(databaseSuite.New(t)), Driver: driver, Backend: "worker"}
	if _, err := runner.ReleaseSettled(t.Context()); err != nil {
		t.Fatal(err)
	}
	if records, workspaces := holdings(t, state); len(records) > 0 || len(workspaces) > 0 {
		t.Fatalf("the leftover is still on the worker: records %v, workspaces %v", records, workspaces)
	}
	if !admittedWithin(t, driver, "next", time.Minute) {
		t.Fatal("the leftover's release did not free the slot")
	}
}

// failingRelease refuses, at the transport, one release request.
type failingRelease struct{ base http.RoundTripper }

func (f failingRelease) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodDelete {
		return nil, errors.New("the worker could not be reached")
	}
	return f.base.RoundTrip(req)
}

// Only what AppTown no longer knows goes. An agent attempt's invocation
// and shared workspace, both named by AppTown's records (from before a
// restart: no process has them in hand), stay; so does a task this process
// has in hand (a judge check under way). Once this process asks to release
// it and the release fails, the next pass removes it.
func TestTheReconcileKeepsWhatAppTownKnows(t *testing.T) {
	t.Parallel()
	f, fake, step := fakeTaskFixture(t, true)
	state := f.onWorker(t, fake)
	driver := f.tasks.Driver.(execution.WorkerTasks)
	previous := driver                      // an AppTown process that stopped
	driver.InHand = &execution.KeysInHand{} // this one, which has none of its keys in hand
	commit := scm.Ref(t.Context(), f.hub, "refs/heads/main")

	const operation, workspace = "step-recorded-agent-1", "step-recorded-a1"
	invocation := leftoverSpec(operation, f.hub, commit)
	invocation.SharedWorkspace = workspace
	if _, err := f.tasks.Records.Reserve(t.Context(), tasks.Admission{Operation: operation, Invocation: "agent-1", Workspace: workspace,
		Owner:   tasks.Owner{ProjectID: f.projectID, RunID: step.RunID, StepID: step.ID, Attempt: 1},
		Backend: f.tasks.Backend, Container: "apptown-task-" + operation, Digest: invocation.Digest(), Source: commit, Admitted: invocation.Admission, Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := previous.Admit(t.Context(), workspace); err != nil || !admitted {
		t.Fatalf("admitting the workspace: %v %v", admitted, err)
	}
	if _, err := previous.Ensure(t.Context(), invocation); err != nil {
		t.Fatal(err)
	}
	ensureOn(t, previous, leftoverSpec("check-leftover", f.hub, commit))
	ensureOn(t, driver, leftoverSpec("check-live", f.hub, commit))

	runner := &TaskRunner{Records: f.tasks.Records, Driver: driver, Backend: f.tasks.Backend}
	if _, err := runner.ReleaseSettled(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, workspaces := holdings(t, state)
	if !slices.Contains(records, operation) || !slices.Contains(workspaces, workspace) || !slices.Contains(records, "check-live") || !slices.Contains(workspaces, "check-live") {
		t.Fatalf("work AppTown knows was released: records %v, workspaces %v", records, workspaces)
	}
	if slices.Contains(records, "check-leftover") || slices.Contains(workspaces, "check-leftover") {
		t.Fatalf("the leftover is still on the worker: records %v, workspaces %v", records, workspaces)
	}

	unreachable := driver
	unreachable.Client = &http.Client{Transport: failingRelease{driver.Client.Transport}}
	if err := unreachable.Release(t.Context(), execution.TaskRef{Key: "check-live"}); err == nil {
		t.Fatal("the release was meant to fail")
	}
	if _, err := runner.ReleaseSettled(t.Context()); err != nil {
		t.Fatal(err)
	}
	if records, workspaces := holdings(t, state); slices.Contains(records, "check-live") || slices.Contains(workspaces, "check-live") ||
		!slices.Contains(records, operation) || !slices.Contains(workspaces, workspace) {
		t.Fatalf("after a failed release: records %v, workspaces %v", records, workspaces)
	}
}
