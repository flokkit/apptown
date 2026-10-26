package maintenance

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// Real mutual-TLS/protocol dispatch, no Docker or agent image needed. Losing the
// listener and restoring the same authenticated endpoint supplies recovery;
// a responding but unusable profile cannot falsely clear the prior outage.
func TestWorkerObservationTracksRealProtocolFailureAndRecovery(t *testing.T) {
	ca := workertest.NewCA(t)
	var mode atomic.Int32
	srv := workertest.StartServerWith(t, ca, &worker.Server{State: t.TempDir(), Profile: execution.WorkerProfile{Name: execution.ProfileWorkerVM}}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch mode.Load() {
			case 1:
				http.Error(w, "private profile failure", http.StatusServiceUnavailable)
				return
			case 2:
				if _, err := w.Write([]byte(`{"name":`)); err != nil {
					t.Error(err)
				}
				return
			case 3:
				if _, err := w.Write([]byte(`{}`)); err != nil {
					t.Error(err)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	client := workertest.Driver(t, ca, srv)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	o := NewWorkerObserver("configured-worker", client, &workerObservationQueue{}, func() time.Time { return now })
	run := func() WorkerSnapshot {
		t.Helper()
		now = now.Add(time.Minute)
		if err := o.Run(t.Context(), "configured-worker"); err != nil {
			t.Fatal(err)
		}
		v, err := o.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := run(); v.Unknown || v.Availability != execution.WorkerAvailable {
		t.Fatal("real worker without agent image not available", v)
	}
	address := srv.Listener.Addr().String()
	srv.Close()
	down := run()
	if down.Unknown || down.Availability != execution.WorkerUnavailable {
		t.Fatal("closed listener not unavailable", down)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	replacement := httptest.NewUnstartedServer(srv.Config.Handler)
	if err := replacement.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	replacement.Listener = listener
	replacement.TLS = srv.TLS.Clone()
	replacement.StartTLS()
	defer replacement.Close()
	for _, m := range []int32{1, 2, 3} {
		mode.Store(m)
		v := run()
		if !v.Unknown || !v.Stale || v.Availability != execution.WorkerUnavailable || v.ObservedAt != down.ObservedAt {
			t.Fatalf("unassessable profile %d cleared outage: %+v", m, v)
		}
	}
	mode.Store(0)
	if v := run(); v.Unknown || v.Stale || v.Availability != execution.WorkerAvailable || !v.ObservedAt.After(down.ObservedAt) {
		t.Fatal("authenticated recovery not fresh", v)
	}
}
