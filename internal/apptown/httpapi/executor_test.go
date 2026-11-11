package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/version"
)

type executorReading struct {
	value maintenance.WorkerSnapshot
	calls int
}

func (s *executorReading) Snapshot(context.Context) (maintenance.WorkerSnapshot, error) {
	s.calls++
	return s.value, nil
}
func TestExecutorReadsStoredWorkerFactsAndMarksVersionMismatch(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	source := &executorReading{value: maintenance.WorkerSnapshot{Configured: true, Availability: execution.WorkerAvailable, Version: "different-build", AttemptedAt: at, ObservedAt: at}}
	h := NewExecutor(source, "https://worker.example:7443", "", nil, nil)
	read := func() ExecutorView {
		t.Helper()
		w := httptest.NewRecorder()
		h.Get(w, httptest.NewRequest("GET", "/executor", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var v ExecutorView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := read()
	if v.HostTarget != "" {
		t.Fatal("a worker's machine is not read, so no host target is named", v)
	}
	if v.Address != "https://worker.example:7443" || v.State != "reachable" || v.Version != "different-build" || !v.VersionMismatch || v.AppTownVersion != version.String() || !v.CheckedAt.Time.Equal(at) {
		t.Fatal(v)
	}
	source.value.Stale = true
	v = read()
	if v.State != "unknown" || v.Version != "" || v.VersionMismatch {
		t.Fatal("old evidence shown as current", v)
	}
	source.value = maintenance.WorkerSnapshot{}
	if v = read(); v.Configured {
		t.Fatal(v)
	}
	if source.calls != 3 {
		t.Fatal("unexpected reads", source.calls)
	}
}

// Without a worker, the executor names the target whose Docker runs the
// work, so the page shows that host's CPU and memory reading.
func TestExecutorNamesTheHostTargetWhoseDockerRunsTheWork(t *testing.T) {
	h := NewExecutor(&executorReading{}, "", "built-in-docker", nil, nil)
	w := httptest.NewRecorder()
	h.Get(w, httptest.NewRequest("GET", "/executor", nil))
	var v ExecutorView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.HostTarget != "built-in-docker" {
		t.Fatal(w.Body.String(), err)
	}
}
