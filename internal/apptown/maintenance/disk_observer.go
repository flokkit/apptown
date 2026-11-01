package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/maintenance/internal/maintenancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/diskspace"
)

// DiskObservation preserves a filesystem's last successful reading when a later
// read fails. A stale reading is evidence of its old state, never current room.
type DiskObservation struct {
	LastFailedAt time.Time
	LastFailure  string
	Reading      diskspace.Reading
	Controller   bool
	Stale        bool
	// Unknown describes the latest attempt, independently of the reading age.
	Unknown  bool
	FailedAt time.Time
	Reason   string
}

type DiskSnapshot struct {
	LastFailedAt time.Time
	LastFailure  string
	Disks        []DiskObservation
	ObservedAt   time.Time
	AttemptedAt  time.Time
	Unknown      bool
	Stale        bool
}

// DiskObserver owns recorded controller/worker readings. Snapshot is passive;
// Run collects evidence under the periodic job's lifetime.
type DiskObserver struct {
	disks Disks
	now   func() time.Time
	db    *database.DB
}

func NewDiskObserver(db *database.DB, disks Disks, now func() time.Time) *DiskObserver {
	return &DiskObserver{db: db, disks: disks, now: clock.Or(now)}
}

// Snapshot returns the latest recorded evidence and its age.
func (o *DiskObserver) Snapshot(ctx context.Context) (DiskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return DiskSnapshot{Unknown: true}, err
	}
	out, err := o.load(ctx)
	if err != nil {
		return DiskSnapshot{Unknown: true}, err
	}
	now := o.now()
	out.Stale = out.ObservedAt.IsZero() || now.Sub(out.ObservedAt) > 15*time.Minute
	out.Unknown = out.ObservedAt.IsZero()
	for i := range out.Disks {
		d := &out.Disks[i]
		at := d.Reading.ReadAt
		d.Stale = at.IsZero() || now.Sub(at) > 15*time.Minute
		d.Unknown = at.IsZero() || now.Sub(at) > 6*time.Hour || d.Reading.Unknown != ""
		if !d.Reading.TotalReadAt.IsZero() && now.Sub(d.Reading.TotalReadAt) > 7*24*time.Hour {
			d.Reading.TotalBytes = 0
		}
		out.Stale = out.Stale || d.Stale
		out.Unknown = out.Unknown || d.Unknown
	}
	return out, nil
}
func (o *DiskObserver) load(ctx context.Context) (DiskSnapshot, error) {
	row, err := maintenancesql.New(o.db.SQL()).Reading(ctx, maintenancesql.ReadingParams{Subject: o.disks.DataDir, Kind: "disks"})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != o.disks.WorkerIdentity {
		return DiskSnapshot{}, nil
	}
	if err != nil {
		return DiskSnapshot{}, err
	}
	var out DiskSnapshot
	err = json.Unmarshal(row.Payload, &out)
	return out, err
}
func (o *DiskObserver) store(ctx context.Context, snapshot DiskSnapshot) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return maintenancesql.New(o.db.SQL()).StoreReading(finish, maintenancesql.StoreReadingParams{ID: ids.New(o.now()), Subject: o.disks.DataDir, Kind: "disks", Identity: o.disks.WorkerIdentity, Payload: raw})
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
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	snapshot, loadErr := o.load(finish)
	if loadErr != nil {
		return loadErr
	}
	snapshot.AttemptedAt = now
	if err := ctx.Err(); err != nil {
		snapshot.Unknown = true
		snapshot.LastFailedAt = now
		snapshot.LastFailure = "The disk check did not finish."
		for i := range snapshot.Disks {
			snapshot.Disks[i].FailedAt = now
			snapshot.Disks[i] = failedDisk(snapshot.Disks[i], now, "The disk check did not finish.")
		}
		if len(snapshot.Disks) == 0 {
			snapshot.Disks = []DiskObservation{failedDisk(DiskObservation{Controller: true, Reading: diskspace.Reading{What: "this AppTown's data folder", Path: o.disks.DataDir, Unknown: "The disk check did not finish", ReadAt: now}}, now, "The disk check did not finish.")}
		}
		return errors.Join(err, o.store(ctx, snapshot))
	}
	prior := snapshot.Disks
	next := DiskSnapshot{LastFailedAt: snapshot.LastFailedAt, LastFailure: snapshot.LastFailure, Disks: []DiskObservation{}, ObservedAt: now, AttemptedAt: now}
	for i, reading := range readings {
		if reading.Unknown == "" {
			if reading.TotalReadAt.IsZero() {
				reading.TotalReadAt = reading.ReadAt
			}
			fresh := DiskObservation{Reading: reading, Controller: i == 0}
			for _, old := range prior {
				if old.Reading.What == reading.What && old.Reading.Path == reading.Path {
					fresh.LastFailedAt, fresh.LastFailure = old.LastFailedAt, old.LastFailure
				}
			}
			next.Disks = append(next.Disks, fresh)
			continue
		}
		next.Unknown = true
		next.LastFailedAt = now
		next.LastFailure = "The disk check failed."
		// The controller is the first reading; individually unread worker
		// filesystems retain only their own matching evidence.
		retained := false
		for j, old := range prior {
			if i == 0 && j != 0 || i != 0 && j == 0 {
				continue
			}
			if old.Reading.What == reading.What && old.Reading.Path == reading.Path {
				old.FailedAt = now
				old.Reason = "The disk check failed."
				old.LastFailedAt = now
				old.LastFailure = old.Reason
				next.Disks = append(next.Disks, old)
				retained = true
			}
		}
		if !retained {
			next.Disks = append(next.Disks, failedDisk(DiskObservation{Reading: reading, Controller: i == 0}, now, "The disk check failed."))
		}
	}
	if workerErr != nil {
		next.Unknown = true
		next.LastFailedAt = now
		next.LastFailure = "The disk check failed."
		if len(prior) > 1 {
			for _, old := range prior[1:] {
				old.FailedAt = now
				old.Reason = "The disk check failed."
				old.LastFailedAt = now
				old.LastFailure = old.Reason
				next.Disks = append(next.Disks, old)
			}
		} else {
			next.Disks = append(next.Disks, failedDisk(DiskObservation{Reading: diskspace.Reading{What: "the worker's disks", Unknown: workerErr.Error(), ReadAt: now}}, now, "The worker disk check failed."))
		}
	}
	return o.store(ctx, next)
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
		reading.FailedAt, reading.Error = disk.FailedAt, disk.Reason
		if disk.Unknown {
			reading.Unknown = "The last successful disk reading is too old or unavailable"
		}
		out = append(out, reading)
	}
	return out
}

func failedDisk(d DiskObservation, at time.Time, reason string) DiskObservation {
	d.FailedAt = at
	d.Reason = reason
	d.LastFailedAt = at
	d.LastFailure = reason
	return d
}
