//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executiontest"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker/workertest"
)

func busyboxSpec(key, output string, exit int, block bool) execution.TaskSpec {
	script := fmt.Sprintf("printf %%s '%s'; exit %d", output, exit)
	if block {
		script = "sleep 300"
	}
	return execution.TaskSpec{Key: key, Image: "busybox:latest", Argv: []string{"sh", "-c", script},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Limits: execution.Limits{CPUs: 1, MemoryBytes: 64 << 20, PIDs: 64}, Expires: time.Now().Add(time.Hour)}}
}

// apptown-worker in a container of its own, with real Docker, keeps the task
// contract; survives a restart of its process with its tasks found again by
// key; refuses another installation; and returns a task's work as a bundle
// AppTown quarantines.
func TestIntegrationAWorkerInItsOwnContainer(t *testing.T) {
	ca := workertest.NewCA(t)
	w := workertest.StartInContainer(t, ca)
	driver := execution.WorkerTasks{URL: w.URL, Client: workertest.Client(t, ca, workertest.Installation)}

	t.Run("the task contract", func(t *testing.T) {
		executiontest.Run(t, executiontest.Harness{Driver: driver, Spec: busyboxSpec, Key: executiontest.UniqueKeys("dind")})
	})

	t.Run("another installation is refused", func(t *testing.T) {
		other := execution.WorkerTasks{URL: w.URL, Client: workertest.Client(t, ca, "inst-2")}
		if _, err := other.Ensure(context.Background(), busyboxSpec("other", "", 0, false)); err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("another installation's request: %v", err)
		}
	})

	t.Run("a restarted worker finds its running task", func(t *testing.T) {
		ctx := context.Background()
		spec := busyboxSpec(executiontest.UniqueKeys("dind")("restart"), "", 0, true)
		created, err := driver.Ensure(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
		t.Cleanup(func() { _ = driver.Release(context.Background(), ref) })
		if err := driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		w.Restart()
		running, err := driver.Observe(ctx, execution.TaskRef{Key: spec.Key})
		if err != nil || running.ID != created.ID || running.State != execution.TaskRunning {
			t.Fatalf("after the restart: %+v %v", running, err)
		}
		again, err := driver.Ensure(ctx, spec)
		if err != nil || again.ID != created.ID {
			t.Fatalf("a retried ensure after the restart: %+v %v", again, err)
		}
		changed := spec
		changed.Argv = []string{"sh", "-c", "echo something else"}
		if _, err := driver.Ensure(ctx, changed); err == nil || !strings.Contains(err.Error(), "different request") {
			t.Fatalf("a different request under the same key after the restart: %v", err)
		}
		if err := driver.Stop(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if stopped, err := driver.Observe(ctx, ref); err != nil || stopped.State != execution.TaskExited {
			t.Fatalf("after stopping: %+v %v", stopped, err)
		}
	})

	t.Run("a task's work comes back as a bundle", func(t *testing.T) {
		ctx := context.Background()
		hub := t.TempDir()
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "base"}} {
			if out, err := gitexec.Run(ctx, hub, args...); err != nil {
				t.Fatal(err, out)
			}
		}
		base, _ := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
		spec := busyboxSpec(executiontest.UniqueKeys("dind")("bundle"), "", 0, false)
		spec.Argv = []string{"sh", "-c", "echo made in a container > /work/made.txt"}
		spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
		created, err := driver.Ensure(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
		t.Cleanup(func() { _ = driver.Release(context.Background(), ref) })
		if err := driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if code, err := driver.Wait(ctx, ref); err != nil || code != 0 {
			t.Fatalf("the task: exit %d %v", code, err)
		}
		bundle := filepath.Join(t.TempDir(), "result.bundle")
		tip, err := driver.Collect(ctx, spec.Key, base, "apptown: made in a container", bundle)
		if err != nil || tip == "" {
			t.Fatalf("collecting: %q %v", tip, err)
		}
		dir, imported, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, "work", "main", base)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if got, _ := gitexec.Output(ctx, dir, "show", imported+":made.txt"); got != "made in a container" {
			t.Fatalf("the task's work: %q", got)
		}
	})
}
