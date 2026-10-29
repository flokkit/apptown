package worker_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// blobOf is a blob the worker's store takes: a tar with one file "content".
func blobOf(t *testing.T, content []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "content", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), buf.Bytes()
}

// A request whose inputs the worker cannot present is refused before
// anything is created: no record, no staged inputs, no task. The worker
// judges sizes by the blobs it holds, whatever the request claims. (A fake
// backend: what is refused here never reaches one.)
func TestAWorkerRefusesInputsItCannotPresentBeforeCreatingAnything(t *testing.T) {
	refuseInputs(t, &executionfake.Tasks{}, fakeSpec)
}

// An ensure under a key whose inputs were staged for another request is
// refused as a conflict, and the inputs staged stay as they were.
func TestInputsStagedForAnotherRequestAreAConflict(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
	ctx := context.Background()
	input := func(content string) execution.Input {
		t.Helper()
		in, err := execution.NewInput("/apptown/in/steering.md", []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	spec := fakeSpec("staged-inputs", "", 0, false)
	spec.Inputs = []execution.Input{input("first")}
	if _, err := driver.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	other := fakeSpec(spec.Key, "", 0, false)
	other.Inputs, other.Request = []execution.Input{input("second")}, spec.Digest()
	if _, err := driver.Ensure(ctx, other); !errors.Is(err, execution.ErrTaskConflict) {
		t.Fatalf("an ensure with other inputs: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(state, "task-inputs", spec.Key, "0")); err != nil || string(b) != "first" {
		t.Fatalf("the staged input: %q %v", b, err)
	}
}

// refuseInputs is the refusal test against a worker running tasks on
// tasks, with requests made by spec.
func refuseInputs(t *testing.T, tasks execution.TaskDriver, spec func(key, output string, exit int, block bool) execution.TaskSpec) {
	t.Helper()
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := workertest.Start(t, ca, state, tasks)
	client := workertest.Client(t, ca, workertest.Installation)
	driver := execution.WorkerTasks{URL: srv.URL, Client: client}
	blobs := worker.SessionClient{URL: srv.URL, Client: client}
	ctx := context.Background()
	put := func(content []byte) string {
		t.Helper()
		sum, body := blobOf(t, content)
		if err := blobs.EnsureBlob(ctx, sum, body); err != nil {
			t.Fatal(err)
		}
		return sum
	}
	// Nothing the task or its workspace would own: a workspace made for a
	// request that is refused has no record to release it by.
	nothingCreated := func(key, workspace string) {
		t.Helper()
		for _, dir := range []string{filepath.Join(state, "records", key+".json"), filepath.Join(state, "task-inputs", key), filepath.Join(state, "task-results", key),
			filepath.Join(state, "workspaces", workspace), filepath.Join(state, "git", workspace+".git"), filepath.Join(state, "workspace-state", workspace)} {
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Errorf("%s: %s was created (%v)", key, dir, err)
			}
		}
		if obs, _ := tasks.Observe(ctx, execution.TaskRef{Key: key}); obs.State != execution.TaskAbsent {
			t.Errorf("%s: a task was created: %+v", key, obs)
		}
	}
	large := put(make([]byte, execution.MaxInputBytes+1))
	sixty := put(bytes.Repeat([]byte("x"), 60<<20))
	var nine []execution.Input
	for i := range 9 {
		nine = append(nine, execution.Input{Blob: sixty, Path: "/apptown/in/part" + strings.Repeat("x", i), Mode: 0o644})
	}
	unknown := strings.Repeat("0", 64)
	hub := sourceRepo(t)
	base, err := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for name, inputs := range map[string][]execution.Input{
		"an unknown blob":     {{Blob: unknown, Path: "/apptown/in/a", Mode: 0o644}},
		"one input too large": {{Blob: large, Path: "/apptown/in/big", Mode: 0o644}},
		"too large together":  nine,
	} {
		// Without a source, with its own workspace, and in a shared one.
		for _, source := range []string{"none", "own", "shared"} {
			key := "refused-" + strings.ReplaceAll(name, " ", "-") + "-" + source
			request := spec(key, "", 0, false)
			request.Inputs = inputs
			workspace := key
			if source != "none" {
				request.Source = &execution.TaskSource{Repository: hub, Commit: base}
			}
			if source == "shared" {
				workspace = key + "-shared"
				request.SharedWorkspace = workspace
			}
			_, err := driver.Ensure(ctx, request)
			if err == nil || !strings.Contains(err.Error(), "400") {
				t.Errorf("%s: %v", key, err)
			}
			if name == "an unknown blob" && !errors.Is(err, execution.ErrMissingBlob) {
				t.Errorf("%s: an unknown blob is not named as missing: %v", key, err)
			}
			nothingCreated(key, workspace)
		}
	}

	// What AppTown's own validation refuses, the worker refuses too, from
	// any client.
	small := put([]byte("small"))
	raw := func(key string, body map[string]any) (int, string) {
		t.Helper()
		request := spec(key, "", 0, false)
		body["key"], body["image"], body["argv"] = key, request.Image, request.Argv
		body["admission"] = map[string]any{"Profile": "worker", "Network": "none"}
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/tasks/"+key, bytes.NewReader(encoded))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		answer, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(answer)
	}
	for name, body := range map[string]map[string]any{
		"an input in the tree":         {"inputs": []map[string]any{{"blob": small, "path": "/work/a", "mode": 0o644}}},
		"an input escaping its folder": {"inputs": []map[string]any{{"blob": small, "path": "/apptown/in/../../etc/x", "mode": 0o644}}},
		"an input with another mode":   {"inputs": []map[string]any{{"blob": small, "path": "/apptown/in/a", "mode": 0o4755}}},
		"a result escaping the tree":   {"workspace": "ws", "results": []map[string]any{{"name": "r", "path": "/work/../etc/passwd", "max": 10}}},
		"a result without a workspace": {"results": []map[string]any{{"name": "r", "path": "/work/r", "max": 10}}},
	} {
		key := "raw-" + strings.ReplaceAll(name, " ", "-")
		// Refused for what is wrong with it, not for something else.
		part := "the task's inputs"
		if _, ok := body["results"]; ok {
			part = "the task's result files"
		}
		if status, answer := raw(key, body); status != http.StatusBadRequest || !strings.Contains(answer, part) {
			t.Errorf("%s: %d %s", name, status, answer)
		}
		nothingCreated(key, key)
	}
}

type labels map[string]map[string]string

func (l labels) Labels(_ context.Context, image string) (map[string]string, error) {
	found, ok := l[image]
	if !ok {
		return nil, errors.New("no such image: " + image)
	}
	return found, nil
}

// The worker reports its agent image's name and labels; a worker that built
// none says so, distinctly. (Labels from a fake; the integration test reads
// them from Docker.)
func TestAWorkerReportsItsAgentImage(t *testing.T) {
	ca := workertest.NewCA(t)
	with := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: &executionfake.Tasks{}, AgentImage: "apptown-agent:abc",
		ImageLabels: labels{"apptown-agent:abc": {"forge.subagents": "reviewer"}}})
	got, err := workertest.Driver(t, ca, with).AgentImage(context.Background())
	if err != nil || got.Name != "apptown-agent:abc" || got.Labels["forge.subagents"] != "reviewer" {
		t.Fatalf("the agent image: %+v %v", got, err)
	}
	without := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir(), Tasks: &executionfake.Tasks{}})
	if _, err := workertest.Driver(t, ca, without).AgentImage(context.Background()); !errors.Is(err, execution.ErrNoAgentImage) {
		t.Fatalf("a worker without an agent image: %v", err)
	}
}
