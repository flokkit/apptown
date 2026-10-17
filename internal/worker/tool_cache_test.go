package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// Every task on the worker mounts the worker's one tool cache, writable, at
// the path the forge points mise's download cache to, so a toolchain's
// release listings and archives are fetched once per worker, not once per
// task. The worker's records, trees and generated files are not in it.
func TestAWorkerTaskSharesTheWorkersToolCache(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	tasks := &mountsSeen{Tasks: &executionfake.Tasks{}}
	state := t.TempDir()
	cache := filepath.Join(t.TempDir(), "tool-cache")
	srv := &worker.Server{Installation: workertest.Installation, State: state, Tasks: tasks, Sessions: consoletest.NewAgent(), ToolCache: cache}
	hs := workertest.StartServer(t, ca, srv)
	driver := execution.WorkerTasks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	ctx := context.Background()
	for _, key := range []string{"script-1", "script-2"} {
		spec := taskWithFiles(key, miseConfig)
		if _, err := driver.Ensure(ctx, spec); err != nil {
			t.Fatal(err)
		}
		mounts := tasks.of(key)
		if !slices.Contains(mounts, cache+":"+execution.ToolCacheMount) {
			t.Fatalf("task %s does not share the tool cache writable: %v", key, mounts)
		}
	}
	if entries, err := os.ReadDir(cache); err == nil && len(entries) > 0 {
		t.Fatalf("the worker wrote into the tool cache: %v", entries)
	}

	// A worker given no tool cache mounts none.
	none := &mountsSeen{Tasks: &executionfake.Tasks{}}
	bare := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Tasks: none, Sessions: consoletest.NewAgent()})
	if _, err := (execution.WorkerTasks{URL: bare.URL, Client: workertest.Client(t, ca, workertest.Installation)}).Ensure(ctx, taskWithFiles("script-3", miseConfig)); err != nil {
		t.Fatal(err)
	}
	for _, m := range none.of("script-3") {
		if strings.HasSuffix(m, ":"+execution.ToolCacheMount) {
			t.Fatalf("a worker without a tool cache mounted one: %v", none.of("script-3"))
		}
	}
}
