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

// CapacitySnapshot contains disk evidence and whole-capacity coverage for one
// exact target configuration. Unknown includes unread resource figures. The
// wrapper time never replaces Disk.ReadAt, which may be older than the wrapper.
type CapacitySnapshot struct {
	Disk       *DiskCapacity
	ObservedAt time.Time
	// AttemptedAt distinguishes a pending first check from a failed check.
	AttemptedAt time.Time
	// Reason is a safe summary of the failed or overdue check.
	Reason      string
	DiskUnknown bool
	Unknown     bool
	Stale       bool
}

type capacityObservation struct {
	revision int64
	snapshot CapacitySnapshot
}

// CapacityObserver collects capacity in background jobs. Pages and Attention
// read the latest recorded evidence without requesting work.
type CapacityObserver struct {
	mu      sync.Mutex
	entries map[ids.ID]*capacityObservation
	now     func() time.Time
	target  func(context.Context, ids.ID) (*Target, error)
	read    func(context.Context, *Target) (TargetCapacity, error)
}

func NewCapacityObserver(service *Service) *CapacityObserver {
	return &CapacityObserver{now: service.now, target: service.Targets.ByID, read: service.targetCapacity}
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
	out := capacitySnapshotAt(e.snapshot, now)
	o.mu.Unlock()
	return out, nil
}

// Allow two missed five-minute checks and two minutes for collection.
const capacityObservationFresh = 12 * time.Minute

func capacitySnapshotAt(s CapacitySnapshot, now time.Time) CapacitySnapshot {
	s.Stale = s.Stale || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > capacityObservationFresh
	if s.Disk != nil {
		disk := *s.Disk
		s.Disk = &disk
		s.Stale = s.Stale || disk.StaleAt(now)
	}
	if s.Stale {
		s.Unknown = true
		if s.ObservedAt.IsZero() && s.AttemptedAt.IsZero() {
			s.Reason = "Waiting for the first capacity check."
		} else if s.Reason == "" {
			s.Reason = "The capacity check is overdue."
		}
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
	if o.entries == nil {
		o.entries = make(map[ids.ID]*capacityObservation)
	}
	if e == nil || e.revision != revision {
		e = &capacityObservation{revision: revision}
		o.entries[id] = e
	}
	e.snapshot.AttemptedAt = now
	if err != nil {
		e.snapshot.Unknown = true
		e.snapshot.DiskUnknown = true
		e.snapshot.Reason = "The host capacity check failed."
		return err
	}
	if reading.Disk == nil || !reading.Disk.Free.Known || !reading.Disk.Total.Known {
		// Preserve a prior pressure condition under unreadable new evidence.
		e.snapshot.Unknown = true
		e.snapshot.DiskUnknown = true
		e.snapshot.Stale = true
		e.snapshot.Reason = "The host disk check failed."
		return nil
	}
	disk := *reading.Disk
	reason := capacityFailure(reading)
	e.snapshot = CapacitySnapshot{Disk: &disk, ObservedAt: reading.ReadAt, AttemptedAt: now, Unknown: reason != "", Reason: reason}
	return nil
}

// Optional figures remain absent when the provider does not offer them. A
// present but unread figure is incomplete coverage, even with a healthy disk.
func capacityFailure(reading TargetCapacity) string {
	for _, resource := range []struct {
		name    string
		reading *ResourceCapacity
	}{{"memory", reading.Memory}, {"CPU", reading.CPU}} {
		r := resource.reading
		if r == nil {
			continue
		}
		if !r.Total.Known {
			return "The host " + resource.name + " total could not be read."
		}
		if !r.Promised.Sum.AppTown.Known || !r.Promised.Sum.Other.Known || r.Promised.Unread.AppTown > 0 || r.Promised.Unread.Other > 0 {
			return "Container " + resource.name + " limits could not all be read."
		}
		if r.InUse != nil && (!r.InUse.AppTown.Known || !r.InUse.Other.Known) {
			return "Container " + resource.name + " use could not all be read."
		}
		if r.HostUsed != nil && !r.HostUsed.Known {
			return "The host's own " + resource.name + " use could not be read."
		}
	}
	for _, t := range reading.Tenants {
		if !t.Used.Known {
			return "Container memory use could not all be read."
		}
		if !t.Promised.Known {
			return "Container memory limits could not all be read."
		}
		if t.CPUUnread > 0 {
			return "Container CPU limits could not all be read."
		}
	}
	return ""
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
