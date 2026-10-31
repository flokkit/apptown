package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
)

type workerDisks struct {
	readings []diskspace.Reading
	err      error
}

func (w workerDisks) Disks(context.Context) ([]diskspace.Reading, error) { return w.readings, w.err }

// AppTown's data folder is read where it is, as a warning only: no new
// work is admitted against it, so it never says refused. The worker's
// readings are its own, as it sent them; a worker that does not answer is
// one reading, not read, and not called refusing.
//
// Kills: AppTown's own reading left refusing; a worker's failure dropped
// or called a refusal.
func TestAppTownsDisksAreEachTheirOwnReading(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	at := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	full := diskspace.Reading{What: "the worker's state folder", Path: "/state", TotalBytes: 100, FreeBytes: 1, Refuses: true, ReadAt: at}
	read := func(disks Disks) []diskspace.Reading {
		observer := NewDiskObserver(disks, func() time.Time { return at })
		if err := observer.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		return observer.Read(t.Context())
	}
	got := read(Disks{DataDir: dir, Worker: workerDisks{readings: []diskspace.Reading{full}}, Now: func() time.Time { return at }})
	if len(got) != 2 || got[0].Path != dir || got[0].TotalBytes <= 0 || got[0].Refuses || got[0].FloorBytes != 0 || got[0].WarningBytes <= 0 || !got[0].ReadAt.Equal(at) || got[1] != full {
		t.Fatalf("readings: %+v", got)
	}
	down := read(Disks{DataDir: dir, Worker: workerDisks{err: errors.New("the worker could not be reached")}})
	if len(down) != 2 || down[1].Unknown == "" || down[1].Refuses {
		t.Fatalf("worker failure: %+v", down)
	}
	if alone := read(Disks{DataDir: dir}); len(alone) != 1 {
		t.Fatalf("no worker: %+v", alone)
	}
}
