package maintenance

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
)

// WorkerProbe observes the authenticated worker protocol, not image readiness.
type WorkerProbe interface {
	Availability(context.Context) (execution.WorkerAvailability, error)
}
type WorkerObservationQueue interface {
	RequestWorkerObservation(context.Context, string) error
}

type WorkerSnapshot struct {
	Identity     string
	Configured   bool
	Availability execution.WorkerAvailability
	ObservedAt   time.Time
	AttemptedAt  time.Time
	Unknown      bool
	Stale        bool
}

// WorkerObserver retains evidence for one immutable configured worker. Replacing
// configuration constructs a new observer; no old result can clear a new source.
type WorkerObserver struct {
	identity  string
	worker    WorkerProbe
	queue     WorkerObservationQueue
	now       func() time.Time
	mu        sync.Mutex
	running   bool
	requested time.Time
	snapshot  WorkerSnapshot
}

func NewWorkerObserver(identity string, worker WorkerProbe, queue WorkerObservationQueue, now func() time.Time) *WorkerObserver {
	return &WorkerObserver{identity: identity, worker: worker, queue: queue, now: clock.Or(now), snapshot: WorkerSnapshot{Identity: identity, Configured: worker != nil, Unknown: worker != nil}}
}

func (o *WorkerObserver) readLocked() WorkerSnapshot {
	out := o.snapshot
	out.Stale = out.Configured && (out.Unknown || out.ObservedAt.IsZero() || o.now().Sub(out.ObservedAt) > 2*time.Minute)
	return out
}

// Snapshot only requests durable work. Failed admission also consumes the
// cadence budget, so repeated page reads cannot hammer an unavailable queue.
func (o *WorkerObserver) Snapshot(ctx context.Context) (WorkerSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return WorkerSnapshot{Unknown: true, Stale: true}, err
	}
	o.mu.Lock()
	due := o.worker != nil && !o.running && (o.requested.IsZero() || o.now().Sub(o.requested) >= time.Minute)
	if due {
		o.requested = o.now()
	}
	out := o.readLocked()
	o.mu.Unlock()
	if !due {
		return out, nil
	}
	var err error
	if o.queue == nil {
		err = errors.New("worker observation queue unavailable")
	} else {
		err = o.queue.RequestWorkerObservation(ctx, o.identity)
	}
	if err != nil {
		o.mu.Lock()
		o.snapshot.Unknown = true
		out = o.readLocked()
		o.mu.Unlock()
	}
	return out, err
}

// Run observes only the source this job was admitted for. A failed/ambiguous
// response retains previous unavailability; it is never recovery evidence.
func (o *WorkerObserver) Run(ctx context.Context, identity string) error {
	o.mu.Lock()
	if o.worker == nil || identity != o.identity || o.running {
		o.mu.Unlock()
		return nil
	}
	o.running = true
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.running = false; o.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state, err := o.worker.Availability(ctx)
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.requested = now
	o.snapshot.AttemptedAt = now
	if ctx.Err() != nil || (state != execution.WorkerAvailable && state != execution.WorkerUnavailable) || (state == execution.WorkerAvailable && err != nil) {
		o.snapshot.Unknown = true
		return nil // Observation failure is coverage, not a reason for an immediate job retry.
	}
	o.snapshot.Availability = state
	o.snapshot.ObservedAt = now
	o.snapshot.Unknown = false
	return nil
}
