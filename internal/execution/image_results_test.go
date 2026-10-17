package execution_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/execution"
)

// buildSpec is a release build as the forge may send it: its source, its
// generated build file and its private daemon, and nothing else.
func buildSpec() execution.TaskSpec {
	return execution.TaskSpec{Key: "build-1", Image: "docker:27-cli", Argv: []string{"docker", "buildx", "build", "."},
		Admission: execution.Admission{Profile: "worker-vm", Network: execution.NetworkDefault,
			Limits: execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 512}, Expires: time.Now().Add(time.Hour)},
		Source:  &execution.TaskSource{Repository: "/repo", Commit: strings.Repeat("a", 40)},
		Files:   []execution.File{{Path: "/forge/build/web.Dockerfile", Content: []byte("FROM busybox\n"), Mode: 0o644}},
		Private: &execution.PrivateDocker{Daemon: true},
		Images:  []execution.ImageResult{{Name: "web", Path: "/forge/state/out/web", Max: 1 << 30}}}
}

// A release build carries no credential or secret: a request declaring
// images that would carry environment, inputs or services (whose
// environment travels with it) is refused before anything is sent to the
// worker, as is one whose images are not in a workspace of its own, made
// from its source, or are declared outside /forge/state, through "..", twice
// or without their admitted size.
func TestABuildRequestCarriesNoCredentialOrSecret(t *testing.T) {
	t.Parallel()
	if err := buildSpec().Validate(time.Now()); err != nil {
		t.Fatalf("a release build with its source, build file and daemon: %v", err)
	}
	input, err := execution.NewInput("/forge/in/token", []byte("glpat-secret"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]func(*execution.TaskSpec){
		"environment": func(s *execution.TaskSpec) { s.Env = []string{"REGISTRY_TOKEN=glpat-secret"} },
		"an input":    func(s *execution.TaskSpec) { s.Inputs = []execution.Input{input} },
		"a service": func(s *execution.TaskSpec) {
			s.Private.Services = []deploy.ServiceSpec{{Name: "db", Image: "postgres", Env: map[string]string{"PASSWORD": "x"}}}
		},
		"no source":        func(s *execution.TaskSpec) { s.Source = nil },
		"a shared space":   func(s *execution.TaskSpec) { s.SharedWorkspace = "step-1" },
		"a .. path":        func(s *execution.TaskSpec) { s.Images[0].Path = "/forge/state/../../etc" },
		"a path in /work":  func(s *execution.TaskSpec) { s.Images[0].Path = "/work/out/web" },
		"no admitted size": func(s *execution.TaskSpec) { s.Images[0].Max = 0 },
		"an image twice":   func(s *execution.TaskSpec) { s.Images = append(s.Images, s.Images[0]) },
		"a path twice": func(s *execution.TaskSpec) {
			s.Images = append(s.Images, execution.ImageResult{Name: "api", Path: s.Images[0].Path, Max: 1})
		},
		"a name with a slash": func(s *execution.TaskSpec) { s.Images[0].Name = "../web" },
		"a dotted name":       func(s *execution.TaskSpec) { s.Images[0].Name = ".." },
		"an uncleaned path":   func(s *execution.TaskSpec) { s.Images[0].Path = "/forge/state/out//web" },
	}
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	driver := execution.WorkerTasks{URL: srv.URL, Client: srv.Client()}
	for what, change := range refused {
		spec := buildSpec()
		change(&spec)
		if err := spec.Validate(time.Now()); err == nil {
			t.Errorf("a build with %s was accepted", what)
		}
		if _, err := driver.Ensure(context.Background(), spec); err == nil {
			t.Errorf("a build with %s was ensured", what)
		}
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("%d requests reached the worker for refused builds", n)
	}
	// A backend that captures no images refuses them.
	if err := (execution.DockerTasks{}).Capabilities().Unclaimed(buildSpec()); !errors.Is(err, execution.ErrUnsupported) {
		t.Errorf("a local backend's answer to images: %v", err)
	}
}

// An image file is read whole or not at all: a reply without its length is
// refused, one cut short fails as a transport error rather than ending as a
// shorter file, a released task's file is gone, images not captured yet
// are said to be so, and only an index or a blob is asked for.
func TestAnImageFileIsReadWholeOrFails(t *testing.T) {
	t.Parallel()
	blob := "blobs/sha256/" + strings.Repeat("b", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tasks/chunked/images/web/" + blob:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("part"))
			w.(http.Flusher).Flush()
		case "/v1/tasks/short/images/web/" + blob:
			// Ten bytes promised, four sent, and the connection closed.
			conn, buf, _ := w.(http.Hijacker).Hijack()
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\npart")
			_ = buf.Flush()
			_ = conn.Close()
		case "/v1/tasks/gone/images/web/" + blob:
			http.Error(w, "the task was released", http.StatusGone)
		case "/v1/tasks/uncaptured/images/web/" + blob, "/v1/tasks/uncaptured/images":
			http.Error(w, "the task's images are not captured: read its images first", http.StatusConflict)
		default:
			http.Error(w, "unexpected "+r.URL.String(), http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	driver := execution.WorkerTasks{URL: srv.URL, Client: srv.Client()}
	open := func(key, file string) (io.ReadCloser, int64, error) {
		return driver.ImageFile(context.Background(), execution.TaskRef{Key: key}, "web", file)
	}
	if _, _, err := open("chunked", blob); !errors.Is(err, execution.ErrWorkerUnavailable) {
		t.Errorf("a reply without its length: %v", err)
	}
	body, n, err := open("short", blob)
	if err != nil || n != 10 {
		t.Fatalf("a reply stating its length: %d %v", n, err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err == nil {
		t.Errorf("a reply cut short read as %q, a success", got)
	}
	if _, _, err := open("gone", blob); !errors.Is(err, execution.ErrImageGone) {
		t.Errorf("a released task's file: %v", err)
	}
	// Images not captured yet are that, never another task under the key.
	_, _, fileErr := open("uncaptured", blob)
	_, listErr := driver.ImageResults(context.Background(), execution.TaskRef{Key: "uncaptured"})
	for _, err := range []error{fileErr, listErr} {
		if err == nil || errors.Is(err, execution.ErrTaskConflict) || !strings.Contains(err.Error(), "has not captured the task's images") {
			t.Errorf("images not captured yet: %v", err)
		}
	}
	for _, file := range []string{"oci-layout", "blobs/sha256/../../../records/x.json", "blobs/sha256/" + strings.Repeat("B", 64), "index.json/"} {
		if _, _, err := open("never", file); err == nil || strings.Contains(err.Error(), "unexpected") {
			t.Errorf("asking for %q: %v", file, err)
		}
	}
}
