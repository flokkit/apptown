package maintenance

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

type workerProbeFunc func(context.Context) (execution.WorkerAvailability, error)

func (f workerProbeFunc) Availability(ctx context.Context) (execution.WorkerAvailability, error) {
	return f(ctx)
}

type workerObservationQueue struct {
	calls int
	err   error
	key   string
}

func (q *workerObservationQueue) RequestWorkerObservation(_ context.Context, key string) error {
	q.calls++
	q.key = key
	return q.err
}
func TestWorkerSnapshotsAreCachedAndUnknownCannotClearUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	state := execution.WorkerUnavailable
	calls := 0
	q := &workerObservationQueue{}
	o := NewWorkerObserver("worker-a", workerProbeFunc(func(context.Context) (execution.WorkerAvailability, error) { calls++; return state, nil }), q, func() time.Time { return now })
	for range 3 {
		v, err := o.Snapshot(t.Context())
		if err != nil || !v.Unknown || !v.Stale || calls != 0 || q.calls != 1 || q.key != "worker-a" {
			t.Fatalf("cold read: %+v %v calls=%d queue=%+v", v, err, calls, q)
		}
	}
	if err := o.Run(t.Context(), "old-worker"); err != nil || calls != 0 {
		t.Fatal("wrong source observed", err, calls)
	}
	if err := o.Run(t.Context(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	down, _ := o.Snapshot(t.Context())
	if down.Unknown || down.Availability != execution.WorkerUnavailable || down.ObservedAt != now {
		t.Fatal(down)
	}
	now = now.Add(time.Minute)
	state = execution.WorkerUnknown
	if err := o.Run(t.Context(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	unknown, _ := o.Snapshot(t.Context())
	if !unknown.Unknown || !unknown.Stale || unknown.Availability != execution.WorkerUnavailable || unknown.ObservedAt != down.ObservedAt || unknown.AttemptedAt != now {
		t.Fatal("unknown cleared or renewed down evidence", unknown)
	}
	now = now.Add(time.Minute)
	state = execution.WorkerAvailable
	if err := o.Run(t.Context(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	recovered, _ := o.Snapshot(t.Context())
	if recovered.Unknown || recovered.Stale || recovered.Availability != execution.WorkerAvailable || recovered.ObservedAt != now {
		t.Fatal(recovered)
	}
	now = now.Add(121 * time.Second)
	stale, _ := o.Snapshot(t.Context())
	if !stale.Stale || calls != 3 || q.calls != 2 {
		t.Fatal("stale read did inline work", stale, calls, q.calls)
	}
	q.err = errors.New("private queue failure")
	now = now.Add(time.Minute)
	failed, err := o.Snapshot(t.Context())
	if err == nil || !failed.Unknown || !failed.Stale || failed.ObservedAt != recovered.ObservedAt {
		t.Fatal("queue failure cleared coverage", failed, err)
	}
}
func TestWorkerObservationIsBoundedAndDoesNotOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &workerObservationQueue{}
		calls := 0
		entered := make(chan struct{})
		done := make(chan error, 1)
		o := NewWorkerObserver("worker", workerProbeFunc(func(ctx context.Context) (execution.WorkerAvailability, error) {
			calls++
			close(entered)
			<-ctx.Done()
			return execution.WorkerAvailable, nil
		}), q, time.Now)
		go func() { done <- o.Run(t.Context(), "worker") }()
		<-entered
		if err := o.Run(t.Context(), "worker"); err != nil {
			t.Fatal(err)
		}
		_, _ = o.Snapshot(t.Context())
		if calls != 1 || q.calls != 0 {
			t.Fatal("overlapping probe or request", calls, q.calls)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		v, _ := o.Snapshot(t.Context())
		if !v.Unknown || !v.ObservedAt.IsZero() {
			t.Fatal("cancelled probe became evidence", v)
		}
	})
}
func TestNoWorkerIsNotApplicableAndNeverQueues(t *testing.T) {
	q := &workerObservationQueue{}
	o := NewWorkerObserver("", nil, q, time.Now)
	v, err := o.Snapshot(t.Context())
	if err != nil || v.Configured || v.Unknown || v.Stale || q.calls != 0 {
		t.Fatal(v, err, q.calls)
	}
}
