//go:build integration

package worker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

func checkContainers(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=apptown.class=check").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// With real Docker: an AppTown that stops waiting for a check (it disconnected,
// or restarted) leaves no container and no folder on the worker.
func TestIntegrationADisconnectedCheckLeavesNothing(t *testing.T) {
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{}}
	hs := workertest.StartServer(t, ca, srv)
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := checks.Run(ctx, execution.Request{Workspace: t.TempDir(), Image: "alpine:3", Argv: []string{"sleep", "60"},
		Network: "none", Timeout: time.Minute, Name: "disconnect"})
	if out.Exited0() {
		t.Fatalf("a check AppTown stopped waiting for passed: %+v", out)
	}
	if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
		entries, _ := os.ReadDir(filepath.Join(srv.State, "checks"))
		return checkContainers(t) == "" && len(entries) == 0
	}) {
		t.Fatalf("left on the worker: containers %q", checkContainers(t))
	}
}

// With real Docker: a check container left by a worker that stopped mid-check
// is removed when the worker starts again, before its deadline; another
// worker's running check on the same Docker is left alone.
func TestIntegrationTheStartSweepRemovesAnInterruptedCheck(t *testing.T) {
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir()}
	id := runCheckContainer(t, srv.CheckOwnerLabel())
	other := otherWorkersCheck(t)
	srv.SweepChecks(context.Background(), docker.CLI{Binary: "docker"}, false)
	if !strings.Contains(checkContainers(t), id[:12]) {
		t.Fatal("a sweep removed a check before its deadline")
	}
	srv.SweepChecks(context.Background(), docker.CLI{Binary: "docker"}, true)
	if strings.Contains(checkContainers(t), id[:12]) {
		t.Fatal("the start's sweep left an interrupted check")
	}
	if !strings.Contains(checkContainers(t), other[:12]) {
		t.Fatal("the start's sweep removed another worker's running check")
	}
}

// runCheckContainer starts a check container, as a worker labels it, with a
// deadline an hour away; it is removed with the test.
func runCheckContainer(t *testing.T, owner string) string {
	t.Helper()
	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	// The id is docker's standard output alone: a pull of alpine:3 is
	// reported on standard error and is not part of it.
	var stderr strings.Builder
	cmd := exec.Command("docker", "run", "-d", "--label", "apptown.class=check", "--label", owner,
		"--label", "apptown.deadline="+future, "alpine:3", "sleep", "300")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	return id
}

// otherWorkersCheck is a running check of another worker on the same Docker.
func otherWorkersCheck(t *testing.T) string {
	t.Helper()
	return runCheckContainer(t, "apptown.worker=another-worker-"+strconv.FormatInt(time.Now().UnixNano(), 10))
}
