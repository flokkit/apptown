package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// imageHub is a repository with one commit holding a Dockerfile.
func imageHub(t *testing.T) (hub, base string) {
	t.Helper()
	hub = t.TempDir()
	files := map[string]string{
		"Dockerfile":   "FROM busybox:1.37\nCOPY greeting.txt /greeting.txt\nCMD [\"cat\", \"/greeting.txt\"]\n",
		"greeting.txt": "hello from a release build\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(hub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "-m", "base"}} {
		if out, err := gitexec.Run(t.Context(), hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	base, err := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return hub, strings.TrimSpace(base)
}

// A worker refuses an image declared through "..", whatever AppTown
// sent, with nothing recorded; and of a captured image it serves its index
// and its blobs, by those names alone: no other file of the layout, nothing
// reached through "..", an escaped separator or another image's name, and
// nothing of an image that was not captured present. Once the task is
// released, a new open is gone. (A fake backend: the
// task's exit is the fake's, its layout written where it would leave it.)
func TestAWorkerServesAnImageByItsIndexAndBlobsAlone(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	srv := workertest.Start(t, ca, state, tasks)
	driver := workertest.Driver(t, ca, srv)
	client := workertest.Client(t, ca, workertest.Installation)
	ctx := context.Background()

	call := func(method, path string, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(got)
	}
	admission := execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)}
	hub, base := imageHub(t)
	spec := execution.TaskSpec{Key: "build", Image: "fake", Argv: []string{"run"}, Admission: admission,
		Source: &execution.TaskSource{Repository: hub, Commit: base},
		Images: []execution.ImageResult{{Name: "web", Path: "/apptown/state/out/web", Max: 1 << 20}, {Name: "api", Path: "/apptown/state/out/api", Max: 1 << 20}}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	escape, _ := json.Marshal(worker.TaskRequest{Key: "escape", Image: "fake", Argv: []string{"run"}, Admission: admission, Workspace: "build", Source: base,
		Images: []execution.ImageResult{{Name: "web", Path: "/apptown/state/../../records", Max: 1 << 20}}})
	if code, body := call(http.MethodPost, "/v1/tasks/escape", string(escape)); code != http.StatusBadRequest || !strings.Contains(body, "images") {
		t.Errorf("an image declared through ..: %d %s", code, body)
	}
	if tasks.Present("escape") {
		t.Error("a task with an image declared through .. was created")
	}
	if _, err := os.Stat(filepath.Join(state, "records", "escape.json")); !os.IsNotExist(err) {
		t.Errorf("a task with an image declared through .. was recorded: %v", err)
	}

	blob := strings.Repeat("b", 64)
	layout := filepath.Join(state, "workspace-state", "build", "out", "web")
	for rel, body := range map[string]string{"oci-layout": "{}", "index.json": `{"manifests":[]}`, "blobs/sha256/" + blob: "layer"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(layout, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(layout, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if code, _ := call(http.MethodGet, "/v1/tasks/build/images/web/index.json", ""); code != http.StatusConflict {
		t.Errorf("an image file before the task's images were captured: %d", code)
	}
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	images, err := driver.ImageResults(ctx, ref)
	if err != nil || images["web"].State != execution.ResultPresent || images["api"].State != execution.ResultAbsent {
		t.Fatalf("the captured images: %+v %v", images, err)
	}
	for file, want := range map[string]string{"index.json": `{"manifests":[]}`, "blobs/sha256/" + blob: "layer"} {
		body, size, err := driver.ImageFile(ctx, ref, "web", file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		got, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(got) != want || size != int64(len(want)) {
			t.Errorf("%s: %q (%d) %v, want %q", file, got, size, err, want)
		}
	}
	for _, path := range []string{
		"/v1/tasks/build/images/web/oci-layout",
		"/v1/tasks/build/images/web/blobs/sha256/../../oci-layout",
		"/v1/tasks/build/images/web/blobs/sha256/..%2F..%2Foci-layout",
		"/v1/tasks/build/images/web/blobs%2Fsha256%2F..%2F..%2F..%2F..%2F..%2Frecords%2Fbuild.json",
		"/v1/tasks/build/images/..%2F..%2Frecords/index.json",
		// The layout as the task left it, uncaptured, from the captured
		// images' folder.
		"/v1/tasks/build/images/..%2F..%2F..%2Fworkspace-state%2Fbuild%2Fout%2Fweb/index.json",
		"/v1/tasks/build/images/api/index.json",
		"/v1/tasks/build/images/other/index.json",
	} {
		if code, body := call(http.MethodGet, path, ""); code == http.StatusOK || strings.Contains(body, `"key"`) || body == "{}" || strings.Contains(body, "manifests") {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	// Released, a new open is gone, and so is the listing.
	if err := driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if body, _, err := driver.ImageFile(ctx, ref, "web", "index.json"); !errors.Is(err, execution.ErrImageGone) {
		if body != nil {
			body.Close()
		}
		t.Errorf("a new open after the release: %v", err)
	}
	if _, err := driver.ImageResults(ctx, ref); !errors.Is(err, execution.ErrImageGone) {
		t.Errorf("the images after the release: %v", err)
	}
}

// A release build's image is not copied when the copy would leave the
// worker's state folder with less than its floor free: the capture records
// it refused, saying what is free, and nothing is written; the build is not
// asked again into a full disk.
//
// Kills: the capture's disk admission removed (the layout is copied).
func TestAnImageWhoseCopyWouldFillTheStateFolderIsRefused(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	const gib = int64(1) << 30
	var free atomic.Int64
	free.Store(50 * gib)
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Tasks: tasks,
		StateDisk: func(string) (diskspace.Space, error) {
			return diskspace.Space{TotalBytes: 100 * gib, FreeBytes: free.Load()}, nil
		}})
	driver := workertest.Driver(t, ca, srv)
	ctx := context.Background()
	hub, base := imageHub(t)
	spec := execution.TaskSpec{Key: "build", Image: "fake", Argv: []string{"run"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Source:    &execution.TaskSource{Repository: hub, Commit: base},
		Images:    []execution.ImageResult{{Name: "web", Path: "/apptown/state/out/web", Max: 1 << 20}}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(state, "workspace-state", "build", "out", "web")
	if err := os.MkdirAll(layout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), []byte(`{"manifests":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	free.Store(5 * gib) // exactly the floor: any copy goes below it
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	images, err := driver.ImageResults(ctx, ref)
	if err != nil || images["web"].State != execution.ResultRefused || !strings.Contains(images["web"].Reason, "5.0 GiB free of 100.0 GiB") {
		t.Fatalf("the captured images: %+v %v", images, err)
	}
	if _, err := os.Stat(filepath.Join(state, "task-results", "build", "images", "web")); !os.IsNotExist(err) {
		t.Errorf("the refused image was copied: %v", err)
	}
}
