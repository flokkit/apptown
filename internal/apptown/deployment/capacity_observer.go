package deployment

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

// CapacityObservationQueue only admits a deduplicated durable observation for
// this target revision. It must never contact a provider on the snapshot path.
type CapacityObservationQueue interface {
	RequestCapacityObservation(context.Context, ids.ID, int64) error
}

// CapacitySnapshot contains disk evidence and whole-capacity coverage for one
// exact target configuration. Unknown includes unread resource figures. The
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

// capacityObservationFresh limits wrapper age; disk evidence has its own longer
// limit because its cache survives wrapper refreshes.
const capacityObservationFresh = 2 * time.Minute

func capacitySnapshotAt(s CapacitySnapshot, now time.Time) CapacitySnapshot {
	s.Stale = s.Stale || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > capacityObservationFresh
	if s.Disk != nil {
		disk := *s.Disk
		s.Disk = &disk
		s.Stale = s.Stale || disk.StaleAt(now)
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
	e.snapshot = CapacitySnapshot{Disk: &disk, ObservedAt: reading.ReadAt, AttemptedAt: now, Unknown: capacityUnknown(reading)}
	return nil
}

// Optional figures remain absent when the provider does not offer them. A
// present but unread figure is incomplete coverage, even with a healthy disk.
func capacityUnknown(reading TargetCapacity) bool {
	for _, r := range []*ResourceCapacity{reading.Memory, reading.CPU} {
		if r == nil {
			continue
		}
		if !r.Total.Known || !r.Promised.Sum.AppTown.Known || !r.Promised.Sum.Other.Known || r.Promised.Unread.AppTown > 0 || r.Promised.Unread.Other > 0 {
			return true
		}
		if r.InUse != nil && (!r.InUse.AppTown.Known || !r.InUse.Other.Known) {
			return true
		}
		if r.HostUsed != nil && !r.HostUsed.Known {
			return true
		}
	}
	for _, t := range reading.Tenants {
		if !t.Used.Known || !t.Promised.Known || t.CPUUnread > 0 {
			return true
		}
	}
	return false
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
