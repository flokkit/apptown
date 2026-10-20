package execution_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/worker/workertest"
)

// A blob the worker removed after AppTown sent it (a worker restart
// removes blobs no chat uses) is sent again, once, and the task is ensured.
// The driver is AppTown's; the worker behind it is the real server, so the
// refusal it retries on is the worker's own.
func TestABlobTheWorkerLostIsSentAgain(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	var lost atomic.Bool
	srv := workertest.StartWith(t, ca, state, tasks, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/tasks/") && lost.CompareAndSwap(false, true) {
				// Between the upload and the ensure, the blobs go.
				_ = os.RemoveAll(filepath.Join(state, "blobs"))
			}
			h.ServeHTTP(w, r)
		})
	})
	driver := workertest.Driver(t, ca, srv)
	in, err := execution.NewInput("/apptown/in/steering.md", []byte("steer"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	spec := execution.TaskSpec{Key: "lost-blob", Image: "fake", Argv: []string{"run"}, Env: []string{"FAKE_OUTPUT=", "FAKE_EXIT=0"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Inputs:    []execution.Input{in}}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("a lost blob was not sent again: %v", err)
	}
	if !lost.Load() {
		t.Fatal("the worker never lost the blob")
	}
	body, err := os.ReadFile(filepath.Join(state, "task-inputs", "lost-blob", "0"))
	if err != nil || string(body) != "steer" {
		t.Fatalf("the staged input: %q %v", body, err)
	}
}

// The first input validation must retain the resend guarantee when a worker
// loses an uploaded blob, including validation at the new preflight route.
func TestReviewFirstInputValidationResendsABlobLostAfterUpload(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	var lost atomic.Bool
	srv := workertest.StartWith(t, ca, state, tasks, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && (r.URL.Path == "/v1/inputs/check" || strings.HasPrefix(r.URL.Path, "/v1/tasks/")) && lost.CompareAndSwap(false, true) {
				// A restart loses the blob before the first input validation:
				// preflight now, or task creation before preflight was added.
				_ = os.RemoveAll(filepath.Join(state, "blobs"))
			}
			h.ServeHTTP(w, r)
		})
	})
	driver := workertest.Driver(t, ca, srv)
	in, err := execution.NewInput("/apptown/in/steering.md", []byte("steer"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	spec := execution.TaskSpec{Key: "lost-blob", Image: "fake", Argv: []string{"run"}, Env: []string{"FAKE_OUTPUT=", "FAKE_EXIT=0"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Inputs:    []execution.Input{in}}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("a lost blob was not sent again: %v", err)
	}
	if !lost.Load() {
		t.Fatal("the worker never lost the blob")
	}
	body, err := os.ReadFile(filepath.Join(state, "task-inputs", "lost-blob", "0"))
	if err != nil || string(body) != "steer" {
		t.Fatalf("the staged input: %q %v", body, err)
	}
}
