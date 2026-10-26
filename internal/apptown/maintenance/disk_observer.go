package maintenance

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/diskspace"
)

// DiskObservation preserves a filesystem's last successful reading when a later
// read fails. A stale reading is evidence of its old state, never current room.
type DiskObservation struct {
	Reading    diskspace.Reading
	Controller bool
	Stale      bool
	// Unknown describes the latest attempt, independently of the reading age.
	Unknown bool
}

type DiskSnapshot struct {
	Disks       []DiskObservation
	ObservedAt  time.Time
	AttemptedAt time.Time
	Unknown     bool
	Stale       bool
}

// DiskObservationQueue schedules the existing maintenance owner's refresh job.
// It must enqueue work only, not read disks or contact the worker inline.
// Unfinished requests share one durable job identity, including while running.
type DiskObservationQueue interface{ RequestDiskObservation(context.Context) error }

// DiskObserver owns cached controller/worker readings. Its snapshot path only
// queues a refresh; Run performs external reads under the job's lifetime.
type DiskObserver struct {
	disks     Disks
	queue     DiskObservationQueue
	now       func() time.Time
	mu        sync.Mutex
	snapshot  DiskSnapshot
	requested time.Time
}

func NewDiskObserver(disks Disks, queue DiskObservationQueue, now func() time.Time) *DiskObserver {
	return &DiskObserver{disks: disks, queue: queue, now: clock.Or(now), snapshot: DiskSnapshot{Unknown: true}}
}

// Snapshot returns current cached evidence. Failed enqueue attempts are also
// rate limited; every caller sees the retained evidence as unassessed meanwhile.
func (o *DiskObserver) Snapshot(ctx context.Context) (DiskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return DiskSnapshot{Unknown: true}, err
	}
	now := o.now()
	o.mu.Lock()
	refresh := o.requested.IsZero() || now.Sub(o.requested) >= time.Minute
	if refresh {
		o.requested = now
	}
	out := o.copyLocked(now)
	o.mu.Unlock()
	if !refresh {
		return out, nil
	}
	var err error
	if o.queue == nil {
		err = errors.New("disk observation queue unavailable")
	} else {
		err = o.queue.RequestDiskObservation(ctx)
	}
	if err != nil {
		o.mu.Lock()
		o.snapshot.Unknown = true
		out = o.copyLocked(now)
		o.mu.Unlock()
	}
	return out, err
}

func (o *DiskObserver) copyLocked(now time.Time) DiskSnapshot {
	out := o.snapshot
	out.Disks = slices.Clone(out.Disks)
	out.Stale = out.ObservedAt.IsZero() || now.Sub(out.ObservedAt) > 2*time.Minute
	for i := range out.Disks {
		// Each filesystem keeps its own time. Refreshing a sibling cannot renew it.
		at := out.Disks[i].Reading.ReadAt
		out.Disks[i].Stale = out.Disks[i].Stale || at.IsZero() || now.Sub(at) > 2*time.Minute
	}
	return out
}

// Run refreshes the complete controller/worker sample. It is called by the
// bounded maintenance job, not by a request handler. Failed worker coverage
// retains its earlier disk warnings rather than erasing them from the view.
func (o *DiskObserver) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	own, worker, workerErr := o.disks.read(ctx)
	readings := []diskspace.Reading{own}
	if workerErr == nil {
		readings = append(readings, worker...)
	}
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requested = now
	o.snapshot.AttemptedAt = now
	if err := ctx.Err(); err != nil {
		o.snapshot.Unknown = true
		return err
	}
	prior := o.snapshot.Disks
	next := DiskSnapshot{Disks: []DiskObservation{}, ObservedAt: now, AttemptedAt: now}
	for i, reading := range readings {
		if reading.Unknown == "" {
			next.Disks = append(next.Disks, DiskObservation{Reading: reading, Controller: i == 0})
			continue
		}
		next.Unknown = true
		// The controller is the first reading; individually unread worker
		// filesystems retain only their own matching evidence.
		retained := false
		for j, old := range prior {
			if i == 0 && j != 0 || i != 0 && j == 0 {
				continue
			}
			if old.Reading.What == reading.What && old.Reading.Path == reading.Path {
				old.Stale = true
				old.Unknown = true
				next.Disks = append(next.Disks, old)
				retained = true
			}
		}
		if !retained {
			next.Disks = append(next.Disks, DiskObservation{Reading: reading, Controller: i == 0, Stale: true, Unknown: true})
		}
	}
	if workerErr != nil {
		next.Unknown = true
		if len(prior) > 1 {
			for _, old := range prior[1:] {
				old.Stale = true
				old.Unknown = true
				next.Disks = append(next.Disks, old)
			}
		} else {
			next.Disks = append(next.Disks, DiskObservation{Reading: diskspace.Reading{What: "the worker's disks", Unknown: workerErr.Error(), ReadAt: now}, Stale: true, Unknown: true})
		}
	}
	o.snapshot = next
	return nil
}
