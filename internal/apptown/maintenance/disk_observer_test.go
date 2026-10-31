package maintenance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
)

type observedWorker struct {
	readings []diskspace.Reading
	err      error
	calls    int
}

func (w *observedWorker) Disks(context.Context) ([]diskspace.Reading, error) {
	w.calls++
	return w.readings, w.err
}

// Polling a cold or expired view schedules work but never reads the worker.
// A failed source retains each old reading's own time until recovery supplies
// fresh evidence, and an unrelated successful controller read cannot renew it.
func TestDiskSnapshotsRefreshBehindTheReadAndRetainFailedWorkerEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	first := now
	worker := &observedWorker{readings: []diskspace.Reading{{What: "worker state", Path: "/state", TotalBytes: 100 << 30, FreeBytes: 2 << 30, Low: true, Refuses: true, ReadAt: first}}}
	observer := NewDiskObserver(Disks{DataDir: t.TempDir(), Worker: worker, Now: func() time.Time { return now }}, func() time.Time { return now })
	for range 3 {
		got, err := observer.Snapshot(t.Context())
		if err != nil || !got.Unknown || !got.Stale || len(got.Disks) != 0 || worker.calls != 0 {
			t.Fatalf("cold read did work or claimed coverage: %+v %v", got, err)
		}
	}
	if err := observer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := observer.Snapshot(t.Context())
	if err != nil || got.Unknown || len(got.Disks) != 2 || got.Disks[0].Reading.Refuses || !got.Disks[1].Reading.Low || got.Disks[1].Stale {
		t.Fatalf("first observation: %+v %v", got, err)
	}
	// Returned slices cannot mutate the owner's retained observation.
	got.Disks[1].Reading.Low = false
	now = now.Add(13 * time.Minute)
	got, err = observer.Snapshot(t.Context())
	if err != nil || !got.Stale || !got.Disks[1].Stale || !got.Disks[1].Reading.Low || worker.calls != 1 {
		t.Fatalf("expired read: %+v %v", got, err)
	}
	worker.err = errors.New("worker unavailable")
	if err := observer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = observer.Snapshot(t.Context())
	if err != nil || !got.Unknown || len(got.Disks) != 2 || !got.Disks[1].Stale || !got.Disks[1].Unknown || got.Disks[1].Reading.ReadAt != first || !got.Disks[1].Reading.Low || got.Disks[0].Reading.ReadAt != now {
		t.Fatalf("failed refresh erased or renewed evidence: %+v %v", got, err)
	}
	worker.err = nil
	now = now.Add(time.Minute)
	worker.readings[0].Low = false
	worker.readings[0].Refuses = false
	worker.readings[0].ReadAt = now
	if err := observer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = observer.Snapshot(t.Context())
	if err != nil || got.Unknown || got.Disks[1].Unknown || got.Disks[1].Stale || got.Disks[1].Reading.Low || got.Disks[1].Reading.ReadAt != now {
		t.Fatalf("recovery not observed: %+v %v", got, err)
	}
}

func TestCanceledBackgroundCheckCannotReportKnownCoverage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	observer := NewDiskObserver(Disks{DataDir: t.TempDir()}, func() time.Time { return now })
	got, err := observer.Snapshot(t.Context())
	if err != nil || !got.Unknown {
		t.Fatalf("passive snapshot: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := observer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh: %v", err)
	}
	got, err = observer.Snapshot(t.Context())
	if err != nil || !got.Unknown || len(got.Disks) != 1 || !got.Disks[0].Unknown {
		t.Fatalf("cancellation did not record unknown coverage: %+v %v", got, err)
	}
}

// These are owned disposable filesystem paths, not injected statfs failures.
// Both worker filesystem readings use the same statfs/NewReading contract as
// the execution adapter; no daemon or remote worker is claimed by this test.
type localDiskWorker struct {
	paths []string
	now   func() time.Time
}

func (w localDiskWorker) Disks(context.Context) ([]diskspace.Reading, error) {
	out := []diskspace.Reading{}
	for i, path := range w.paths {
		space, err := diskspace.Statfs(path)
		out = append(out, diskspace.NewReading([]string{"worker state", "worker Docker"}[i], path, space, err, w.now()))
	}
	return out, nil
}

func TestDiskUnreadStatePreservesEachFilesystemAndRecovers(t *testing.T) {
	for _, failed := range []int{0, 1, 2} {
		t.Run([]string{"controller", "worker state", "worker Docker"}[failed], func(t *testing.T) {
			now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
			worker := localDiskWorker{paths: paths[1:], now: func() time.Time { return now }}
			observer := NewDiskObserver(Disks{DataDir: paths[0], Worker: worker, Now: worker.now}, worker.now)
			if err := observer.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			first, err := observer.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if first.Unknown || len(first.Disks) != 3 {
				t.Fatalf("initial readings: %+v", first)
			}
			if err := os.Remove(paths[failed]); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if err := observer.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := observer.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !got.Unknown || !got.Disks[failed].Unknown || !got.Disks[failed].Stale || got.Disks[failed].Reading != first.Disks[failed].Reading {
				t.Fatalf("failed read lost its state or previous evidence: %+v", got)
			}
			for i, d := range got.Disks {
				if i != failed && (d.Unknown || d.Stale || d.Reading.ReadAt != now) {
					t.Fatalf("failure contaminated sibling %d: %+v", i, d)
				}
			}
			if err := os.Mkdir(paths[failed], 0700); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if err := observer.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err = observer.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got.Unknown || got.Disks[failed].Unknown || got.Disks[failed].Stale || got.Disks[failed].Reading.ReadAt != now {
				t.Fatalf("recovery did not replace unread evidence: %+v", got)
			}
			now = now.Add(13 * time.Minute)
			got, err = observer.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !got.Disks[failed].Stale || got.Disks[failed].Unknown {
				t.Fatalf("age alone became a failed read: %+v", got)
			}
		})
	}
}
