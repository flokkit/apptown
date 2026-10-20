package maintenance

import (
	"context"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/diskspace"
)

// Disks reads the filesystems this AppTown writes to (its data folder: the
// database's when it is built in, repositories, workspaces, caches,
// backups kept here) and those of its worker (its state folder, and the
// Docker its tasks run on), each its own reading: different filesystems
// are never summed into one figure. A deployment target's own disk is the
// target's capacity (deployment.Service.Capacity). Each is read on the
// request, so a reading carries its time; one that cannot be read says why.
type Disks struct {
	DataDir string
	// Worker reads the worker's own disks (execution.WorkerTasks.Disks);
	// nil for an AppTown with no worker.
	Worker interface {
		Disks(context.Context) ([]diskspace.Reading, error)
	}
	Now func() time.Time
}

// Read reads each filesystem now.
func (d Disks) Read(ctx context.Context) []diskspace.Reading {
	now := clock.Or(d.Now)
	space, err := diskspace.Statfs(d.DataDir)
	own := diskspace.NewReading("this AppTown's data folder", d.DataDir, space, err, now())
	// No new work is admitted against AppTown's own folder: its reading
	// warns (Low), and refuses nothing.
	own.Refuses, own.FloorBytes = false, 0
	out := []diskspace.Reading{own}
	if d.Worker == nil {
		return out
	}
	worker, err := d.Worker.Disks(ctx)
	if err != nil {
		// Not read from here: the worker reads and admits its own.
		return append(out, diskspace.Reading{What: "the worker's disks", Unknown: err.Error(), ReadAt: now()})
	}
	return append(out, worker...)
}
