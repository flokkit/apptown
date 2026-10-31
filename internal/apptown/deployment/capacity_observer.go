package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// CapacityObservationQueue only admits a deduplicated durable observation for
// this target revision. It must never contact a provider on the snapshot path.
type CapacityObservationQueue interface {
	RequestCapacityObservation(context.Context, ids.ID, int64) error
}

// CapacitySnapshot is disk evidence for one exact target configuration. The
// wrapper time never replaces Disk.ReadAt, which may be older than the wrapper.
type CapacitySnapshot struct {
	Disk       *DiskCapacity
	ObservedAt time.Time
	// AttemptedAt distinguishes a pending first check from a failed check.
	AttemptedAt time.Time
	Unknown     bool
	Stale       bool
}

type capacityObservation struct {
	revision  int64
	requested time.Time
	snapshot  CapacitySnapshot
}

// CapacityObserver supplies background disk observations for fixed deployment
// hosts. Detailed capacity pages retain the existing synchronous owner operation.
type CapacityObserver struct {
	mu      sync.Mutex
	entries map[ids.ID]*capacityObservation
	queue   CapacityObservationQueue
	now     func() time.Time
	target  func(context.Context, ids.ID) (*Target, error)
	read    func(context.Context, *Target) (TargetCapacity, error)
}

func NewCapacityObserver(service *Service, queue CapacityObservationQueue) *CapacityObserver {
	return &CapacityObserver{queue: queue, now: service.now, target: service.Targets.ByID, read: service.targetCapacity}
}

// Snapshot takes the current authorized target record. A changed configuration
// starts unassessed rather than inheriting the previous target's disk warning.
func (o *CapacityObserver) Snapshot(ctx context.Context, target *Target) (CapacitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CapacitySnapshot{Unknown: true, Stale: true}, err
	}
	now := o.now()
	o.mu.Lock()
	if o.entries == nil {
		o.entries = make(map[ids.ID]*capacityObservation)
	}
	e := o.entries[target.ID]
	if e == nil || e.revision != target.SettingsRevision {
		e = &capacityObservation{revision: target.SettingsRevision, snapshot: CapacitySnapshot{Unknown: true}}
		o.entries[target.ID] = e
	}
	request := e.requested.IsZero() || now.Sub(e.requested) >= time.Minute
	if request {
		e.requested = now
	}
	out := capacitySnapshotAt(e.snapshot, now)
	o.mu.Unlock()
	if !request {
		return out, nil
	}
	var err error
	if o.queue == nil {
		err = errors.New("capacity observation queue unavailable")
	} else {
		err = o.queue.RequestCapacityObservation(ctx, target.ID, target.SettingsRevision)
	}
	if err != nil {
		o.mu.Lock()
		e.snapshot.Unknown = true
		out = capacitySnapshotAt(e.snapshot, now)
		o.mu.Unlock()
	}
	return out, err
}

func capacitySnapshotAt(s CapacitySnapshot, now time.Time) CapacitySnapshot {
	s.Stale = s.Stale || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > 2*time.Minute
	if s.Disk != nil {
		disk := *s.Disk
		s.Disk = &disk
		s.Stale = s.Stale || disk.ReadAt.IsZero() || now.Sub(disk.ReadAt) > 2*time.Minute
	}
	return s
}

// Run executes under the durable job's lifetime. An obsolete job cannot publish
// a late reading for a replacement target configuration.
func (o *CapacityObserver) Run(ctx context.Context, id ids.ID, revision int64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target, err := o.target(ctx, id)
	if err == nil && target.SettingsRevision != revision {
		return nil
	}
	var reading TargetCapacity
	if err == nil {
		reading, err = o.read(ctx, target)
	}
	if err == nil {
		current, lookupErr := o.target(ctx, id)
		if lookupErr != nil {
			err = lookupErr
		} else if current.SettingsRevision != revision {
			return nil
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	e := o.entries[id]
	if e == nil || e.revision != revision {
		return err
	}
	e.requested = now
	e.snapshot.AttemptedAt = now
	if err != nil {
		e.snapshot.Unknown = true
		return err
	}
	if reading.Disk == nil || !reading.Disk.Free.Known || !reading.Disk.Total.Known {
		// Preserve a prior pressure condition under unreadable new evidence.
		e.snapshot.Unknown = true
		e.snapshot.Stale = true
		return nil
	}
	disk := *reading.Disk
	e.snapshot = CapacitySnapshot{Disk: &disk, ObservedAt: reading.ReadAt, AttemptedAt: now}
	return nil
}

const CapacityObservationJob = "capacity-observation"

// RunJob validates the durable subject before allowing any provider read.
func (o *CapacityObserver) RunJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	rawID, rawRevision, ok := strings.Cut(key, "/")
	id, idErr := ids.Parse(rawID)
	revision, revisionErr := strconv.ParseInt(rawRevision, 10, 64)
	if !ok || idErr != nil || id.IsZero() || revisionErr != nil || revision < 0 {
		return nil, jobs.Permanent(errors.New("invalid capacity observation identity"))
	}
	return nil, o.Run(ctx, id, revision)
}
