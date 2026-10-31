package maintenance

import (
	"context"
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

// DiskObserver owns recorded controller/worker readings. Snapshot is passive;
// Run collects evidence under the periodic job's lifetime.
type DiskObserver struct {
	disks    Disks
	now      func() time.Time
	mu       sync.Mutex
	snapshot DiskSnapshot
}

func NewDiskObserver(disks Disks, now func() time.Time) *DiskObserver {
	return &DiskObserver{disks: disks, now: clock.Or(now), snapshot: DiskSnapshot{Unknown: true}}
}

// Snapshot returns the latest recorded evidence and its age.
func (o *DiskObserver) Snapshot(ctx context.Context) (DiskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return DiskSnapshot{Unknown: true}, err
	}
	now := o.now()
	o.mu.Lock()
	out := o.copyLocked(now)
	o.mu.Unlock()
	return out, nil
}

func (o *DiskObserver) copyLocked(now time.Time) DiskSnapshot {
	out := o.snapshot
	out.Disks = slices.Clone(out.Disks)
	out.Stale = out.ObservedAt.IsZero() || now.Sub(out.ObservedAt) > 12*time.Minute
	for i := range out.Disks {
		// Each filesystem keeps its own time. Refreshing a sibling cannot renew it.
		at := out.Disks[i].Reading.ReadAt
		out.Disks[i].Stale = out.Disks[i].Stale || at.IsZero() || now.Sub(at) > 12*time.Minute
		out.Stale = out.Stale || out.Disks[i].Stale
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
	o.snapshot.AttemptedAt = now
	if err := ctx.Err(); err != nil {
		o.snapshot.Unknown = true
		for i := range o.snapshot.Disks {
			o.snapshot.Disks[i].Unknown = true
			o.snapshot.Disks[i].Stale = true
		}
		if len(o.snapshot.Disks) == 0 {
			o.snapshot.Disks = []DiskObservation{{Controller: true, Unknown: true, Stale: true, Reading: diskspace.Reading{What: "this AppTown's data folder", Path: o.disks.DataDir, Unknown: "The disk check did not finish", ReadAt: now}}}
		}
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

// Read returns the latest recorded disks for infrastructure pages.
func (o *DiskObserver) Read(ctx context.Context) []diskspace.Reading {
	snapshot, err := o.Snapshot(ctx)
	if err != nil {
		return []diskspace.Reading{{Unknown: err.Error()}}
	}
	out := make([]diskspace.Reading, 0, len(snapshot.Disks))
	for _, disk := range snapshot.Disks {
		reading := disk.Reading
		if disk.Unknown || disk.Stale {
			reading.Unknown = "The latest disk check failed or is out of date"
		}
		out = append(out, reading)
	}
	return out
}
