//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// With real Docker and the real task driver: a worker that stops and starts
// again, over the same state and the same Docker, runs its start-up sweeps
// as forge-worker does, and keeps every task's generated files, with no chat
// holding anything: a created task starts and reads its original files, a
// running one keeps their bytes, and an exited one is observed and
// collected. The collected commits hold none of the generated files, and a
// released task's files are gone.
func TestIntegrationAWorkerRestartKeepsItsTasksFiles(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	// The containers this test makes are its own: named in its namespace,
	// and released by it.
	namespace := fmt.Sprintf("filesrestart%d", time.Now().UnixNano()%1_000_000_000)
	// Whatever the test leaves, whichever worker was serving, goes with it:
	// only this namespace's containers.
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=forge-task-"+namespace+"-").Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	})
	// Another worker's running check on the same Docker: this worker's start
	// sweeps must leave it.
	other := otherWorkersCheck(t)
	config := execution.File{Path: "/etc/mise/config.toml", Content: []byte("[settings]\nnode.mirror_url = \"https://mirror.invalid/node/\"\n"), Mode: 0o644}
	start := func() (*worker.Server, execution.WorkerTasks, func()) {
		srv := &worker.Server{Installation: workertest.Installation, State: state, Tasks: execution.DockerTasks{Namespace: namespace},
			Checks: execution.Runner{}, Previews: docker.CLI{Binary: "docker"}, Sessions: consoletest.NewAgent()}
		// What forge-worker runs as it starts.
		srv.SweepChecks(context.Background(), docker.CLI{Binary: "docker"}, true)
		srv.SweepPreviews(context.Background(), true)
		if err := srv.ReapSessions(context.Background()); err != nil {
			t.Fatal(err)
		}
		hs := workertest.StartServer(t, ca, srv)
		return srv, execution.WorkerTasks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}, hs.Close
	}
	_, driver, stop := start()
	ctx := context.Background()
	hub := sourceRepo(t)
	base, err := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)
	task := func(key, script string) (execution.TaskSpec, execution.TaskRef) {
		t.Helper()
		spec := execution.TaskSpec{Key: key, Image: "alpine:3", Argv: []string{"sh", "-c", script},
			Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
			Files:     []execution.File{config}, Source: &execution.TaskSource{Repository: hub, Commit: base}}
		created, err := driver.Ensure(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		return spec, execution.TaskRef{Key: key, ProviderID: created.ID}
	}
	created, createdRef := task(namespace+"-created", "cp /etc/mise/config.toml /work/seen.toml")
	_, runningRef := task(namespace+"-running", "sleep 300")
	exited, exitedRef := task(namespace+"-exited", "cp /etc/mise/config.toml /work/seen.toml")
	for _, ref := range []execution.TaskRef{runningRef, exitedRef} {
		if err := driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	if code, err := driver.Wait(ctx, exitedRef); err != nil || code != 0 {
		t.Fatalf("the exited task: %d %v", code, err)
	}
	// The task's files are kept in its folder by their order.
	filesOf := func(key string) string {
		t.Helper()
		path := filepath.Join(state, "task-files", key, "0")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("task %s has no generated files: %v", key, err)
		}
		return path
	}
	runningFile := filesOf(runningRef.Key)

	// The worker stops and starts again over the same state and Docker.
	stop()
	_, driver, _ = start()

	if body, err := os.ReadFile(runningFile); err != nil || string(body) != string(config.Content) {
		t.Fatalf("a running task's files after the restart: %q %v", body, err)
	}
	if obs, err := driver.Observe(ctx, runningRef); err != nil || obs.State != execution.TaskRunning {
		t.Fatalf("the running task after the restart: %+v %v", obs, err)
	}
	if obs, err := driver.Observe(ctx, exitedRef); err != nil || obs.State != execution.TaskExited || obs.ExitCode != 0 {
		t.Fatalf("the exited task after the restart: %+v %v", obs, err)
	}
	if err := driver.Start(ctx, createdRef); err != nil {
		t.Fatalf("the created task after the restart: %v", err)
	}
	if code, err := driver.Wait(ctx, createdRef); err != nil || code != 0 {
		t.Fatalf("the created task ran: %d %v", code, err)
	}
	collect := func(spec execution.TaskSpec) {
		t.Helper()
		result := filepath.Join(t.TempDir(), "result.bundle")
		tip, err := driver.Collect(ctx, spec.Key, base, "forge: the task's work", result)
		if err != nil || tip == "" {
			t.Fatalf("collecting %s: %q %v", spec.Key, tip, err)
		}
		ref := "refs/test/" + spec.Key
		if out, err := gitexec.Run(ctx, hub, "fetch", "-q", result, "refs/heads/work:"+ref); err != nil {
			t.Fatal(err, out)
		}
		files, err := gitexec.Output(ctx, hub, "ls-tree", "-r", "--name-only", ref)
		if err != nil || strings.TrimSpace(files) != "seen.toml" {
			t.Fatalf("the commit collected from %s holds %q (%v)", spec.Key, files, err)
		}
		seen, err := gitexec.Output(ctx, hub, "show", ref+":seen.toml")
		if err != nil || strings.TrimSpace(seen) != strings.TrimSpace(string(config.Content)) {
			t.Fatalf("%s read %q, not its original files (%v)", spec.Key, seen, err)
		}
	}
	collect(created)
	collect(exited)

	for _, ref := range []execution.TaskRef{createdRef, runningRef, exitedRef} {
		if err := driver.Release(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(state, "task-files", ref.Key)); !os.IsNotExist(err) {
			t.Fatalf("a released task's files are kept: %s %v", ref.Key, err)
		}
	}
	if !strings.Contains(checkContainers(t), other[:12]) {
		t.Fatal("this worker's start sweeps removed another worker's running check")
	}
}
