package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

type checkSourceTasks struct {
	executionfake.Tasks
	spec execution.TaskSpec
}

func (d *checkSourceTasks) Ensure(ctx context.Context, s execution.TaskSpec) (execution.TaskObservation, error) {
	d.spec = s
	return d.Tasks.Ensure(ctx, s)
}
func (d *checkSourceTasks) Start(ctx context.Context, r execution.TaskRef) error {
	if err := os.WriteFile(filepath.Join(d.spec.Workspace, "answer.json"), []byte(`{"verdict":"pass"}`), 0o600); err != nil {
		return err
	}
	return d.Tasks.Start(ctx, r)
}
func TestWorkerCheckHasReadOnlySourceBesideWritableWorkAndCollectsItsResult(t *testing.T) {
	ca := workertest.NewCA(t)
	tasks := &checkSourceTasks{}
	s := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Tasks: tasks, Sessions: consoletest.NewAgent()}
	server := workertest.StartServer(t, ca, s)
	driver := execution.WorkerTasks{URL: server.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	repo := t.TempDir()
	commits := gittest.BareRepository(t, repo, "main", gittest.Commit{Message: "source", Files: map[string]string{"README.md": "exact source"}})
	spec := execution.TaskSpec{Key: "isolated-check", Image: "fixture", Argv: []string{"pi"}, Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault, Limits: execution.Limits{WallClock: time.Minute}}, Source: &execution.TaskSource{Repository: repo, Commit: commits[0]}, SourceAtSrc: true, Results: []execution.Result{{Name: "verdict", Path: "/work/answer.json", Max: 1024}}}
	obs, err := driver.Ensure(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: obs.ID}
	t.Cleanup(func() { _ = driver.Release(context.Background(), ref) })
	if tasks.spec.ReadOnlySource == "" || tasks.spec.ReadOnlySource == tasks.spec.Workspace || tasks.spec.WorkspaceRO {
		t.Fatalf("source and scratch are not separated: %+v", tasks.spec)
	}
	b, err := os.ReadFile(filepath.Join(tasks.spec.ReadOnlySource, "README.md"))
	if err != nil || string(b) != "exact source" {
		t.Fatalf("source: %q %v", b, err)
	}
	if err := driver.Start(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Wait(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	files, err := driver.Results(t.Context(), ref)
	if err != nil || string(files["verdict"].Content) != `{"verdict":"pass"}` {
		t.Fatalf("scratch result: %+v %v", files, err)
	}
	if _, err := os.Stat(filepath.Join(tasks.spec.ReadOnlySource, "answer.json")); !os.IsNotExist(err) {
		t.Fatalf("result contaminated committed source: %v", err)
	}
}
