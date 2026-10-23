//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// With real Docker: a task and a check on the worker read their generated
// configuration (here, mise's system configuration) at its path, read-only,
// and the task's files are gone once it is released.
func TestIntegrationAWorkerTaskAndCheckReadTheirGeneratedFiles(t *testing.T) {
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Tasks: execution.DockerTasks{},
		Checks: execution.Runner{}}
	hs := workertest.StartServer(t, ca, srv)
	client := workertest.Client(t, ca, workertest.Installation)
	driver := execution.WorkerTasks{URL: hs.URL, Client: client}
	ctx := context.Background()
	config := execution.File{Path: "/etc/mise/config.toml", Content: []byte("[settings]\nnode.mirror_url = \"https://mirror.invalid/node/\"\n"), Mode: 0o644}

	key := fmt.Sprintf("files-%d", time.Now().UnixNano()%1_000_000_000)
	spec := execution.TaskSpec{Key: key, Image: diskspace.DFImage, Argv: []string{"sh", "-c", "cat /etc/mise/config.toml; echo changed > /etc/mise/config.toml && echo writable"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)}, Files: []execution.File{config}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: created.ID}
	t.Cleanup(func() { _ = driver.Release(context.Background(), ref) })
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Wait(ctx, ref); err != nil {
		t.Fatal(err)
	}
	page, err := driver.Output(ctx, ref, execution.OutputRead{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page.Data), "node.mirror_url") || strings.Contains(string(page.Data), "writable") {
		t.Fatalf("the task's output: %s", page.Data)
	}

	checks := worker.Checks{URL: hs.URL, Client: client}
	out := checks.Run(ctx, execution.Request{Workspace: t.TempDir(), Image: diskspace.DFImage, Argv: []string{"cat", "/etc/mise/config.toml"},
		Network: "none", Timeout: time.Minute, Name: "files", Files: []execution.File{config}})
	if !out.Exited0() || !strings.Contains(string(out.Output), "node.mirror_url") {
		t.Fatalf("the check's output: %+v %s", out, out.Output)
	}
}
