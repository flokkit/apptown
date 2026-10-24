package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// An agent step attempt's invocations share one workspace: each task names
// it, sees the same files, and releasing a task leaves the workspace.
// Removing the workspace is refused while any task in it is not released,
// across a worker service restart (its records and the workspace are kept
// on disk), and succeeds once none remains; removing it again is accepted.
func TestASharedWorkspaceOutlivesItsTasksUntilReleased(t *testing.T) {
	ca := workertest.NewCA(t)
	state, fake := t.TempDir(), &executionfake.Tasks{}
	first := workertest.Start(t, ca, state, fake)
	driver := workertest.Driver(t, ca, first)
	ctx := context.Background()
	hub := sourceRepo(t)
	base, _ := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
	const workspace = "step-7-a1"
	invocation := func(key string) execution.TaskSpec {
		spec := fakeSpec(key, "", 0, false)
		spec.Source, spec.SharedWorkspace = &execution.TaskSource{Repository: hub, Commit: base}, workspace
		return spec
	}
	work, gitDir := filepath.Join(state, "workspaces", workspace), filepath.Join(state, "git", workspace+".git")
	agent := invocation("step-7-a1-agent")
	if _, err := driver.Ensure(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "workspaces", agent.Key)); !os.IsNotExist(err) {
		t.Fatalf("the task was given a workspace of its own: %v", err)
	}
	// What the first invocation left is there for the next.
	if err := os.WriteFile(filepath.Join(work, "made.txt"), []byte("by the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := driver.Release(ctx, execution.TaskRef{Key: agent.Key}); err != nil {
		t.Fatal(err)
	}
	retry := invocation("step-7-a1-agent-retry")
	if _, err := driver.Ensure(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(work, "made.txt")); err != nil || string(body) != "by the agent\n" {
		t.Fatalf("the shared workspace after its first task's release: %q %v", body, err)
	}
	if err := driver.ReleaseWorkspace(ctx, workspace); !errors.Is(err, execution.ErrWorkspaceInUse) {
		t.Fatalf("removing a workspace with a task in it: %v", err)
	}

	// The worker service restarts over the same state.
	first.Close()
	driver = workertest.Driver(t, ca, workertest.Start(t, ca, state, fake))
	if err := driver.ReleaseWorkspace(ctx, workspace); !errors.Is(err, execution.ErrWorkspaceInUse) {
		t.Fatalf("after a restart, removing a workspace with a task in it: %v", err)
	}
	if again, err := driver.Ensure(ctx, retry); err != nil || again.State != execution.TaskCreated {
		t.Fatalf("the task after the restart: %+v %v", again, err)
	}
	if body, err := os.ReadFile(filepath.Join(work, "made.txt")); err != nil || string(body) != "by the agent\n" {
		t.Fatalf("the shared workspace after the restart: %q %v", body, err)
	}
	if err := driver.Release(ctx, execution.TaskRef{Key: retry.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("releasing the last task removed the shared workspace: %v", err)
	}
	for range 2 {
		if err := driver.ReleaseWorkspace(ctx, workspace); err != nil {
			t.Fatalf("removing the workspace: %v", err)
		}
	}
	for _, dir := range []string{work, gitDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s after the workspace's release: %v", dir, err)
		}
	}
}

// Only a worker shares workspaces: the local drivers refuse a shared
// workspace before creating anything, and a worker needs the source commit
// the workspace is made from. (A task that owns its workspace takes it along
// when released: TestSourceAndResultsTravelAsBundles.)
func TestOnlyAWorkerSharesWorkspaces(t *testing.T) {
	spec := fakeSpec("k-local", "", 0, false)
	spec.SharedWorkspace = "step-1-a1"
	if _, err := (&executionfake.Tasks{}).Ensure(context.Background(), spec); !errors.Is(err, execution.ErrUnsupported) {
		t.Fatalf("the fake with a shared workspace: %v", err)
	}
	if _, err := (execution.DockerTasks{ToolVersions: stock.Versions()}).Ensure(context.Background(), spec); !errors.Is(err, execution.ErrUnsupported) {
		t.Fatalf("local Docker with a shared workspace: %v", err)
	}
	if err := (execution.DockerTasks{ToolVersions: stock.Versions()}).ReleaseWorkspace(context.Background(), "step-1-a1"); !errors.Is(err, execution.ErrUnsupported) {
		t.Fatalf("local Docker releasing a workspace: %v", err)
	}
	if _, err := (execution.WorkerTasks{URL: "https://127.0.0.1:1"}).Ensure(context.Background(), spec); !errors.Is(err, execution.ErrUnsupported) {
		t.Fatalf("a shared workspace without a source: %v", err)
	}
}
