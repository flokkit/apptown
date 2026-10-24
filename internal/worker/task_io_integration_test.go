//go:build integration

package worker_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/runtimeimages"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// removeNamespace removes, when the test ends, the containers this test
// made: only those named in its namespace.
func removeNamespace(t *testing.T, namespace string) {
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=apptown-task-"+namespace+"-").Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	})
}

// putInput puts content to the worker as a blob and names it as an input
// at path with mode. AppTown's constructor, whose input carries its blob
// for the driver to send, is unexported until agent steps build inputs;
// the driver's resending of a carried blob is tested beside
// it, in internal/execution.
func putInput(t *testing.T, blobs worker.SessionClient, path string, content []byte, mode os.FileMode) execution.Input {
	t.Helper()
	sum, body := blobOf(t, content)
	if err := blobs.PutBlob(context.Background(), sum, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	return execution.Input{Blob: sum, Path: path, Mode: mode}
}

// With real Docker and the real task driver behind a worker: a task reads
// its inputs at their paths, read-only and with their modes; a Fresh result
// seeded in its source is gone before its command runs; the files it
// declared come back each in its own state, captured when it exited, so a
// later change to the tree does not reach them and a worker service restart
// answers the same; its workspace's state folder is kept beside the tree,
// never collected, and goes with the workspace.
func TestIntegrationATaskReadsItsInputsAndLeavesItsResultFiles(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	namespace := fmt.Sprintf("taskio%d", time.Now().UnixNano()%1_000_000_000)
	removeNamespace(t, namespace)
	start := func() (execution.WorkerTasks, func()) {
		srv := &worker.Server{State: state, Tasks: execution.DockerTasks{Namespace: namespace}}
		hs := workertest.StartServer(t, ca, srv)
		return workertest.Driver(t, ca, hs), hs.Close
	}
	driver, stop := start()
	ctx := context.Background()

	// The source holds a stale verdict, which the task must never see.
	hub := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hub, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hub, ".apptown", "review.json"), []byte(`{"verdict":"stale"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "-m", "base"}} {
		if out, err := gitexec.Run(ctx, hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	base, err := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	blobs := worker.SessionClient{URL: driver.URL, Client: driver.Client}
	input := func(path, body string, mode os.FileMode) execution.Input {
		t.Helper()
		return putInput(t, blobs, path, []byte(body), mode)
	}
	script := `
test -e /work/.apptown/review.json && echo FRESH-KEPT
cat /apptown/in/steering.md; echo
/apptown/in/bin/tool
cat /apptown/in/resources/a.md; echo
echo "MODES $(stat -c %a /apptown/in/bin/tool) $(stat -c %a /apptown/in/steering.md)"
(echo x > /apptown/in/steering.md) 2>/dev/null && echo INPUT-WRITABLE
(echo x > /apptown/in/bin/tool) 2>/dev/null && echo INPUT-WRITABLE
(rm -f /apptown/in/resources/a.md) 2>/dev/null && echo INPUT-REMOVABLE
mkdir -p /work/.apptown && echo '{"verdict":"pass"}' > /work/.apptown/review.json
mkdir -p /apptown/state/sessions && echo '{"type":"session"}' > /apptown/state/sessions/s.jsonl
head -c 100 /dev/zero > /apptown/state/big
ln -s /etc/passwd /work/link.json
ln -s / /work/out
mkdir /work/adir
echo done
`
	spec := execution.TaskSpec{Key: namespace + "-task", Image: runtimeimages.DF(), Argv: []string{"sh", "-c", script},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Source:    &execution.TaskSource{Repository: hub, Commit: base},
		Inputs: []execution.Input{
			input("/apptown/in/steering.md", "steer this way", 0o644),
			input("/apptown/in/bin/tool", "#!/bin/sh\necho tool ran\n", 0o755),
			input("/apptown/in/resources/a.md", "resource a", 0o644),
		},
		Results: []execution.Result{
			{Name: "verdict", Path: "/work/.apptown/review.json", Max: 1024, Fresh: true},
			{Name: "transcript", Path: "/apptown/state/sessions/s.jsonl", Max: 1024},
			{Name: "absent", Path: "/work/nothing.json", Max: 1024},
			{Name: "big", Path: "/apptown/state/big", Max: 10},
			{Name: "link", Path: "/work/link.json", Max: 1024},
			{Name: "escape", Path: "/work/out/etc/passwd", Max: 1 << 20},
			{Name: "folder", Path: "/work/adir", Max: 1024},
		}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if _, err := driver.Results(ctx, ref); err == nil || !strings.Contains(err.Error(), "not exited") {
		t.Fatalf("the results of a task that has not run: %v", err)
	}
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if code, err := driver.Wait(ctx, ref); err != nil || code != 0 {
		t.Fatalf("the task: %d %v", code, err)
	}
	page, err := driver.Output(ctx, ref, execution.OutputRead{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(page.Data)
	for _, want := range []string{"steer this way", "tool ran", "resource a", "MODES 755 644", "done"} {
		if !strings.Contains(out, want) {
			t.Errorf("the task's output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"FRESH-KEPT", "INPUT-WRITABLE", "INPUT-REMOVABLE"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the task's output says %s:\n%s", unwanted, out)
		}
	}

	want := map[string]execution.ResultFile{
		"verdict":    {State: execution.ResultPresent, Content: []byte("{\"verdict\":\"pass\"}\n"), Size: 19},
		"transcript": {State: execution.ResultPresent, Content: []byte("{\"type\":\"session\"}\n"), Size: 19},
		"absent":     {State: execution.ResultAbsent},
		"big":        {State: execution.ResultTooLarge, Size: 100},
		"link":       {State: execution.ResultRefused, Reason: execution.RefusedSymlink},
		"escape":     {State: execution.ResultRefused, Reason: execution.RefusedSymlink},
		"folder":     {State: execution.ResultRefused, Reason: execution.RefusedNotRegular},
	}
	check := func(when string) {
		t.Helper()
		got, err := driver.Results(ctx, ref)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if len(got) != len(want) {
			t.Errorf("%s: %d results, want %d: %+v", when, len(got), len(want), got)
		}
		for name, w := range want {
			g, ok := got[name]
			if !ok || g.State != w.State || g.Reason != w.Reason || g.Size != w.Size || string(g.Content) != string(w.Content) {
				t.Errorf("%s: the result %s is %+v, want %+v", when, name, g, w)
			}
		}
	}
	check("after the task exited")

	// The tree changes after the task exited: what was captured stays.
	tree := filepath.Join(state, "workspaces", spec.Key)
	if err := os.WriteFile(filepath.Join(tree, ".apptown", "review.json"), []byte("changed later"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The worker service stops and starts again over the same state.
	stop()
	driver, _ = start()
	check("after a worker restart")
	if again, err := driver.Ensure(ctx, spec); err != nil || again.ID != created.ID {
		t.Fatalf("the same request ensured after the restart: %+v %v", again, err)
	}
	// The state folder is kept beside the tree, on the worker, after the
	// task and the restart.
	stateDir := filepath.Join(state, "workspace-state", spec.Key)
	if body, err := os.ReadFile(filepath.Join(stateDir, "sessions", "s.jsonl")); err != nil || !strings.Contains(string(body), "session") {
		t.Fatalf("the workspace's state after the task: %q %v", body, err)
	}

	// What is collected is the tree alone: no state, no inputs.
	bundle := filepath.Join(t.TempDir(), "result.bundle")
	tip, err := driver.Collect(ctx, spec.Key, base, "apptown: the task's work", bundle)
	if err != nil || tip == "" {
		t.Fatalf("collecting: %q %v", tip, err)
	}
	if out, err := gitexec.Run(ctx, hub, "fetch", "-q", bundle, "refs/heads/work:refs/test/collected"); err != nil {
		t.Fatal(err, out)
	}
	files, err := gitexec.Output(ctx, hub, "ls-tree", "-r", "--name-only", "refs/test/collected")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Fields(files) {
		if strings.Contains(name, "s.jsonl") || name == "big" || strings.Contains(name, "steering") || strings.Contains(name, "tool") {
			t.Errorf("the collected commit holds %s", name)
		}
	}

	if err := driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{stateDir, filepath.Join(state, "task-inputs", spec.Key), filepath.Join(state, "task-results", spec.Key)} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("%s outlived its task's release: %v", dir, err)
		}
	}
}

// With real Docker behind the worker: requests over the input bounds, with
// invalid paths or naming a blob the worker lacks create no container.
func TestIntegrationAWorkerRefusesInputsBeforeCreatingAContainer(t *testing.T) {
	namespace := fmt.Sprintf("inputsrefused%d", time.Now().UnixNano()%1_000_000_000)
	removeNamespace(t, namespace)
	refuseInputs(t, execution.DockerTasks{Namespace: namespace}, busyboxSpec)
}

// With real Docker: the worker reports the labels its Docker holds for its
// agent image, and says it has none when the image is not there.
func TestIntegrationAWorkerReportsItsAgentImageLabels(t *testing.T) {
	ca := workertest.NewCA(t)
	image := fmt.Sprintf("forge-test-agent-image:%d", time.Now().UnixNano()%1_000_000_000)
	build := exec.Command("docker", "build", "-q", "-t", image, "-")
	build.Stdin = strings.NewReader("FROM " + runtimeimages.DF() + "\nLABEL forge.subagents=reviewer apptown.pi.version=1.2.3\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a labelled image: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })
	images := worker.DockerImages{Binary: "docker"}
	hs := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Tasks: execution.DockerTasks{}, AgentImage: image, ImageLabels: images})
	got, err := workertest.Driver(t, ca, hs).AgentImage(context.Background())
	if err != nil || got.Name != image || got.Labels["forge.subagents"] != "reviewer" || got.Labels["apptown.pi.version"] != "1.2.3" {
		t.Fatalf("the agent image: %+v %v", got, err)
	}
	missing := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Tasks: execution.DockerTasks{}, AgentImage: image + "-absent", ImageLabels: images})
	if _, err := workertest.Driver(t, ca, missing).AgentImage(context.Background()); !errors.Is(err, execution.ErrNoAgentImage) {
		t.Fatalf("an agent image not on the worker's Docker: %v", err)
	}
}
