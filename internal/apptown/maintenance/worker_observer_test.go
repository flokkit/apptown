package maintenance

import (
	"context"
	"errors"
	"testing"
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
	o := NewWorkerObserver(databaseSuite.New(t), "worker-a", workerProbeFunc(func(context.Context) (execution.WorkerAvailability, error) { calls++; return state, nil }), q, func() time.Time { return now })
	for range 3 {
		v, err := o.Snapshot(t.Context())
		if err != nil || !v.Unknown || !v.Stale || calls != 0 || q.calls != 0 {
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
	now = now.Add(16 * time.Minute)
	stale, _ := o.Snapshot(t.Context())
	if !stale.Stale || calls != 3 || q.calls != 0 {
		t.Fatal("stale read did inline work", stale, calls, q.calls)
	}
	q.err = errors.New("private queue failure")
	if err := o.Request(t.Context()); err == nil {
		t.Fatal("queue failure was lost")
	}
	fresh := NewWorkerObserver(o.db, o.identity, o.worker, q, o.now)
	retained, err := fresh.Snapshot(t.Context())
	if err != nil || retained.Availability != recovered.Availability || !retained.ObservedAt.Equal(recovered.ObservedAt) || q.calls != 1 {
		t.Fatal("restart or admission failure lost the stored reading", retained, err)
	}

}
func TestWorkerObservationIsBoundedAndDoesNotOverlap(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := &workerObservationQueue{}
	o := NewWorkerObserver(databaseSuite.New(t), "worker", workerProbeFunc(func(ctx context.Context) (execution.WorkerAvailability, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("worker check is unbounded")
		}
		close(entered)
		<-ctx.Done()
		return execution.WorkerAvailable, nil
	}), q, time.Now)
	go func() { done <- o.Run(ctx, "worker") }()
	<-entered
	if err := o.Run(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Snapshot(t.Context()); err != nil || q.calls != 0 {
		t.Fatal("read queued work", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	v, err := o.Snapshot(t.Context())
	if err != nil || !v.Unknown || !v.ObservedAt.IsZero() || v.Failure != "" || !v.AttemptedAt.IsZero() {
		t.Fatal("cancelled probe became evidence", v, err)
	}
}

func TestNoWorkerIsNotApplicableAndNeverQueues(t *testing.T) {
	q := &workerObservationQueue{}
	o := NewWorkerObserver(databaseSuite.New(t), "", nil, q, time.Now)
	v, err := o.Snapshot(t.Context())
	if err != nil || v.Configured || v.Unknown || v.Stale || q.calls != 0 {
		t.Fatal(v, err, q.calls)
	}
}
