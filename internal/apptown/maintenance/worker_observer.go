package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/maintenance/internal/maintenancesql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

// WorkerProbe observes the authenticated worker protocol, not image readiness.
type WorkerProbe interface {
	Availability(context.Context) (execution.WorkerAvailability, error)
}
type workerQueue interface {
	RequestWorkerObservation(context.Context, string) error
}

type WorkerSnapshot struct {
	Identity                                 string
	Configured                               bool
	Availability                             execution.WorkerAvailability
	ObservedAt, AttemptedAt, LastAvailableAt time.Time
	Failure                                  string
	Unknown, Stale                           bool
}

// WorkerObserver persists observations for one immutable worker configuration.
// Only Request admits work; Snapshot is a local stored read.
type WorkerObserver struct {
	db       *database.DB
	identity string
	worker   WorkerProbe
	queue    workerQueue
	now      func() time.Time
	mu       sync.Mutex
	running  bool
}

func NewWorkerObserver(db *database.DB, identity string, worker WorkerProbe, queue workerQueue, now func() time.Time) *WorkerObserver {
	return &WorkerObserver{db: db, identity: identity, worker: worker, queue: queue, now: clock.Or(now)}
}
func (o *WorkerObserver) load(ctx context.Context) (WorkerSnapshot, error) {
	out := WorkerSnapshot{Identity: o.identity, Configured: o.worker != nil, Unknown: o.worker != nil}
	if o.worker == nil {
		return out, nil
	}
	r, err := maintenancesql.New(o.db.SQL()).Reading(ctx, maintenancesql.ReadingParams{Subject: "instance", Kind: "worker"})
	if errors.Is(err, sql.ErrNoRows) || err == nil && r.Identity != o.identity {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(r.Payload, &out)
	return out, err
}
func (o *WorkerObserver) Snapshot(ctx context.Context) (WorkerSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return WorkerSnapshot{Unknown: true, Stale: true}, err
	}
	out, err := o.load(ctx)
	if err != nil {
		return WorkerSnapshot{Configured: o.worker != nil, Unknown: true, Stale: true}, err
	}
	out.Stale = out.Configured && (out.Unknown || out.ObservedAt.IsZero() || o.now().Sub(out.ObservedAt) > 15*time.Minute)
	out.Unknown = out.Configured && (out.Unknown || out.ObservedAt.IsZero() || o.now().Sub(out.ObservedAt) > 6*time.Hour)
	return out, nil
}
func (o *WorkerObserver) Request(ctx context.Context) error {
	if o.worker == nil {
		return nil
	}
	if o.queue == nil {
		return errors.New("worker observation queue unavailable")
	}
	return o.queue.RequestWorkerObservation(ctx, o.identity)
}
func (o *WorkerObserver) Run(ctx context.Context, identity string) error {
	o.mu.Lock()
	if o.worker == nil || identity != o.identity || o.running {
		o.mu.Unlock()
		return nil
	}
	o.running = true
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.running = false; o.mu.Unlock() }()
	prior, err := o.load(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state, checkErr := o.worker.Availability(ctx)
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	prior.AttemptedAt = o.now()
	prior.Failure = ""
	if ctx.Err() != nil || state != execution.WorkerAvailable && state != execution.WorkerUnavailable || state == execution.WorkerAvailable && checkErr != nil {
		prior.Unknown = true
		prior.Failure = "The worker protocol check failed. Check the worker address and credentials."
	} else {
		prior.Availability = state
		prior.ObservedAt = o.now()
		prior.Unknown = false
		if state == execution.WorkerAvailable {
			prior.LastAvailableAt = o.now()
		} else {
			prior.Failure = "The worker did not accept the protocol check. Check the worker address and credentials."
		}
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return maintenancesql.New(o.db.SQL()).StoreReading(finish, maintenancesql.StoreReadingParams{ID: ids.New(o.now()), Subject: "instance", Kind: "worker", Identity: o.identity, Payload: raw})
}
