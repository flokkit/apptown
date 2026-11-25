package worker_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/internal/gitexec"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// askWorker uses the worker's owner API without waiting for a transport timer.
func askWorker(t *testing.T, s *worker.Server, method, path string, body any, waiting bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&raw).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &raw)
	req.Header.Set("X-AppTown-Worker-Protocol", strconv.Itoa(worker.Protocol))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: workertest.Installation}}}}
	if waiting {
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		req = req.WithContext(ctx)
	}
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	return out
}
func workerAdmission(t *testing.T, s *worker.Server, key string, waiting bool) execution.AdmissionAnswer {
	t.Helper()
	out := askWorker(t, s, http.MethodPost, "/v1/admissions/"+key, nil, waiting)
	var got execution.AdmissionAnswer
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &got) != nil {
		t.Fatalf("admission: %d %s", out.Code, out.Body)
	}
	return got
}
func TestWorkerSlotsWaitForReleaseAndFirstWritesNeedGrants(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	fake := &executionfake.Tasks{}
	s := &worker.Server{Installation: workertest.Installation, State: state, Tasks: fake, TaskLimit: 2, StateDisk: workertest.Disk}
	for _, key := range []string{"one", "two"} {
		if got := workerAdmission(t, s, key, false); !got.Admitted {
			t.Fatal(got)
		}
		spec := execution.WorkerTaskRequest{Key: key, Image: "fake", Argv: []string{"true"}, Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone}}
		if got := askWorker(t, s, http.MethodPost, "/v1/tasks/"+key, spec, false); got.Code != http.StatusOK {
			t.Fatalf("ensure: %d %s", got.Code, got.Body)
		}
	}
	if got := workerAdmission(t, s, "three", true); got.Admitted || got.Ahead != 0 {
		t.Fatal(got)
	}
	spec := execution.WorkerTaskRequest{Key: "ungranted", Image: "fake", Argv: []string{"true"}, Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone}}
	got := askWorker(t, s, http.MethodPost, "/v1/tasks/ungranted", spec, false)
	var refusal execution.Refusal
	if got.Code != http.StatusTooManyRequests || json.Unmarshal(got.Body.Bytes(), &refusal) != nil || refusal.Code != "busy" {
		t.Fatalf("ungranted: %d %s", got.Code, got.Body)
	}
	if obs, err := fake.Observe(t.Context(), execution.TaskRef{Key: spec.Key}); err != nil || obs.State != execution.TaskAbsent {
		t.Fatalf("ungranted task: %+v %v", obs, err)
	}
	if _, err := os.Stat(filepath.Join(state, "records", spec.Key+".json")); !os.IsNotExist(err) {
		t.Fatalf("ungranted record: %v", err)
	}
	done := make(chan execution.AdmissionAnswer, 1)
	go func() { done <- workerAdmission(t, s, "three", false) }()
	if got := askWorker(t, s, http.MethodDelete, "/v1/tasks/one", nil, false); got.Code != http.StatusNoContent {
		t.Fatalf("release: %s", got.Body)
	}
	select {
	case got := <-done:
		if !got.Admitted {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("release did not admit the waiter")
	}
}

func TestWorkerRebuildsBothWorkspaceSlots(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	state, fake := t.TempDir(), &executionfake.Tasks{}
	server := &worker.Server{Installation: workertest.Installation, State: state, Tasks: fake, TaskLimit: 2}
	first := workertest.StartServer(t, ca, server)
	driver := workertest.Driver(t, ca, first)
	hub := sourceRepo(t)
	commit, err := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first-workspace", "second-workspace"} {
		spec := fakeSpec(key, "", 0, false)
		spec.Source = &execution.TaskSource{Repository: hub, Commit: commit}
		if _, err := executiontest.Ensure(t.Context(), driver, spec); err != nil {
			t.Fatal(err)
		}
	}
	first.Close()
	server = &worker.Server{Installation: workertest.Installation, State: state, Tasks: fake, TaskLimit: 2, StateDisk: workertest.Disk}
	if got := workerAdmission(t, server, "third-workspace", true); got.Admitted {
		t.Fatal("restart lost workspace capacity")
	}
	if got := askWorker(t, server, http.MethodDelete, "/v1/tasks/first-workspace", nil, false); got.Code != http.StatusNoContent {
		t.Fatalf("release: %d %s", got.Code, got.Body)
	}
	if got := workerAdmission(t, server, "third-workspace", false); !got.Admitted {
		t.Fatal("release did not free rebuilt holder")
	}
}

type delayedCheckTransport struct {
	base  http.RoundTripper
	admit func()
	check func(context.Context)
	asks  int
}

func (d *delayedCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/v1/admissions/") {
		d.asks++
		if d.asks == 1 {
			d.admit()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"admitted":false,"ahead":0}`)), Header: make(http.Header)}, nil
		}
	}
	if req.URL.Path == "/v1/checks" {
		d.check(req.Context())
	}
	return d.base.RoundTrip(req)
}

type capCheckExecutor struct {
	run func(context.Context, execution.Request) execution.Outcome
}

func (c capCheckExecutor) Run(ctx context.Context, _ string, req execution.Request) execution.Outcome {
	return c.run(ctx, req)
}

func TestWorkerCheckWaitStartsItsTimeoutAfterAdmission(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	now := clock.Or(nil)()
	initial := now
	ran := false
	server := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Now: func() time.Time { return now }, TaskLimit: 1,
		Checks: capCheckExecutor{run: func(_ context.Context, req execution.Request) execution.Outcome {
			ran = true
			if req.Timeout != time.Minute {
				t.Errorf("lost check timeout: %s", req.Timeout)
			}
			return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
		}}}
	srv := workertest.StartServer(t, ca, server)
	client := workertest.Client(t, ca, workertest.Installation)
	client.Timeout = 0
	transport := &delayedCheckTransport{base: client.Transport, admit: func() {
		if ran {
			t.Fatal("check ran before admission")
		}
		now = now.Add(2 * time.Minute)
	}, check: func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(now.Add(time.Minute+10*time.Minute)) {
			t.Errorf("check deadline before admission: %s at %s", deadline, now)
		}
	}}
	client.Transport = transport
	out := (worker.Checks{URL: srv.URL, Client: client, Now: func() time.Time { return now }}).Run(t.Context(), execution.Request{Image: "fake", Argv: []string{"true"}, Network: "none", Timeout: time.Minute})
	if !out.Exited0() || !ran || transport.asks != 2 || !now.Equal(initial.Add(2*time.Minute)) {
		t.Fatalf("delayed check: %+v, asks %d", out, transport.asks)
	}
}
