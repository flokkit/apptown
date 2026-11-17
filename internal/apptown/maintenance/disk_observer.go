package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/apptown/maintenance/internal/maintenancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"

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
// bounded maintenance job, not by a request handler. Failed reads retain their
// earlier evidence; successful siblings are stored even when the job expires.
func (o *DiskObserver) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	own, worker, workerErr := o.disks.read(ctx)
	databaseBytes, databaseErr := maintenancesql.New(o.db.SQL()).DatabaseBytes(ctx)
	databaseCategory := diskspace.Category{Name: "Database", Bytes: databaseBytes, ReadAt: o.now()}
	if databaseErr != nil {
		databaseCategory.Bytes = 0
		databaseCategory.Unknown = "The database could not be measured."
	}
	own.Categories = append(own.Categories, databaseCategory)
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	readings := append([]diskspace.Reading{own}, o.disks.local(ctx)...)
	controllerCount := len(readings)
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
	prior := snapshot.Disks
	next := DiskSnapshot{LastFailedAt: snapshot.LastFailedAt, LastFailure: snapshot.LastFailure, Disks: []DiskObservation{}, ObservedAt: now, AttemptedAt: now}
	for i, reading := range readings {
		if reading.Unknown == "" {
			if reading.TotalReadAt.IsZero() {
				reading.TotalReadAt = reading.ReadAt
			}
			fresh := DiskObservation{Reading: reading, Controller: i < controllerCount}
			for _, old := range prior {
				if old.Reading.What == reading.What {
					fresh.LastFailedAt, fresh.LastFailure = old.LastFailedAt, old.LastFailure
				}
			}
			next.Disks = append(next.Disks, fresh)
			continue
		}
		next.Unknown = true
		next.LastFailedAt = now
		next.LastFailure = "The disk reading is unavailable."
		reason := "The disk reading is unavailable."
		if reading.Unknown == "Docker did not answer in time" {
			reason = "Docker did not answer in time."
		}
		// The controller is the first reading; individually unread worker
		// filesystems retain only their own matching evidence.
		retained := false
		for _, old := range prior {
			if (i < controllerCount) != old.Controller {
				continue
			}
			if old.Reading.What == reading.What {
				old.FailedAt = now
				old.Reason = reason
				old.LastFailedAt = now
				old.LastFailure = old.Reason
				next.Disks = append(next.Disks, old)
				retained = true
			}
		}
		if !retained {
			next.Disks = append(next.Disks, failedDisk(DiskObservation{Reading: reading, Controller: i < controllerCount}, now, reason))
		}
	}
	if workerErr != nil {
		reason := "The worker disk reading is unavailable."
		if errors.Is(workerErr, context.DeadlineExceeded) {
			reason = "The worker did not answer in time."
		}
		next.Unknown = true
		next.LastFailedAt = now
		next.LastFailure = "The disk reading is unavailable."
		hasPriorWorker := false
		for _, old := range prior {
			if !old.Controller {
				hasPriorWorker = true
			}
		}
		if hasPriorWorker {
			for _, old := range prior {
				if old.Controller {
					continue
				}
				old.FailedAt = now
				old.Reason = reason
				old.LastFailedAt = now
				old.LastFailure = old.Reason
				next.Disks = append(next.Disks, old)
			}
		} else {
			next.Disks = append(next.Disks, failedDisk(DiskObservation{Reading: diskspace.Reading{What: "the worker's disks", Unknown: workerErr.Error(), ReadAt: now}}, now, reason))
		}
	}
	return errors.Join(ctx.Err(), o.store(ctx, next))
}

// Read returns the latest recorded disks for infrastructure pages.
func (o *DiskObserver) Read(ctx context.Context) []diskspace.Reading {
	snapshot, err := o.Snapshot(ctx)
	if err != nil {
		return []diskspace.Reading{{Unknown: err.Error()}}
	}
	out := make([]diskspace.Reading, 0, len(snapshot.Disks))
	for _, disk := range snapshot.DisplayDisks() {
		reading := disk.Reading
		reading.Stale = disk.Stale
		reading.FailedAt, reading.Error = disk.FailedAt, disk.Reason
		if !disk.Known() {
			reading.Unknown = "There is no recent disk reading."
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

// Known reports whether this disk has current, successful space evidence.
func (d DiskObservation) Known() bool {
	return !d.Unknown && !d.Stale && !d.Reading.ReadAt.IsZero() && d.Reading.Unknown == "" && !d.FailedAt.After(d.Reading.ReadAt)
}

// DisplayDisks joins worker roles on the same identified filesystem. Stored
// roles remain separate so a failed probe retains its own successful evidence.
func (s DiskSnapshot) DisplayDisks() []DiskObservation {
	out := make([]DiskObservation, 0, len(s.Disks))
	for _, disk := range s.Disks {
		joined := false
		for i, prior := range out {
			if disk.Controller || prior.Controller || disk.Reading.Filesystem == "" || disk.Reading.Filesystem != prior.Reading.Filesystem {
				continue
			}
			categories := append(append([]diskspace.Category(nil), prior.Reading.Categories...), disk.Reading.Categories...)
			if disk.Known() && !prior.Known() || disk.Known() == prior.Known() && disk.Reading.ReadAt.After(prior.Reading.ReadAt) {
				out[i] = disk
			}
			out[i].Reading.What = "the worker's disk (state folder and Docker data: tasks, images, private Docker)"
			out[i].Reading.Categories = categories
			joined = true
			break
		}
		if !joined {
			out = append(out, disk)
		}
	}
	return out
}
