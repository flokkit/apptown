package maintenance

import (
	"context"
	"errors"
	"os"
	"reflect"
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
	observer := NewDiskObserver(databaseSuite.New(t), Disks{DataDir: t.TempDir(), Worker: worker, Now: func() time.Time { return now }}, func() time.Time { return now })
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
	if readings := observer.Read(t.Context()); len(readings) != 2 || readings[1].Stale {
		t.Fatalf("fresh display: %+v", readings)
	}
	// Returned slices cannot mutate the owner's retained observation.
	got.Disks[1].Reading.Low = false
	now = now.Add(30 * time.Minute)
	got, err = observer.Snapshot(t.Context())
	if err != nil || !got.Stale || !got.Disks[1].Stale || !got.Disks[1].Reading.Low || worker.calls != 1 {
		t.Fatalf("expired read: %+v %v", got, err)
	}
	if readings := observer.Read(t.Context()); len(readings) != 2 || !readings[1].Stale {
		t.Fatalf("old display: %+v", readings)
	}
	worker.err = errors.New("worker unavailable")
	if err := observer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = observer.Snapshot(t.Context())
	if err != nil || got.Unknown || len(got.Disks) != 2 || !got.Disks[1].Stale || got.Disks[1].Unknown || got.Disks[1].FailedAt != now || got.Disks[1].Reading.ReadAt != first || !got.Disks[1].Reading.Low || got.Disks[0].Reading.ReadAt != now {
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
	observer := NewDiskObserver(databaseSuite.New(t), Disks{DataDir: t.TempDir()}, func() time.Time { return now })
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
	if err != nil || !got.Unknown || len(got.Disks) != 0 || !got.AttemptedAt.IsZero() || got.LastFailure != "" {
		t.Fatalf("cancellation changed the reading: %+v %v", got, err)
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
			observer := NewDiskObserver(databaseSuite.New(t), Disks{DataDir: paths[0], Worker: worker, Now: worker.now}, worker.now)
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
			if got.Unknown || got.Disks[failed].Unknown || got.Disks[failed].Stale || got.Disks[failed].FailedAt != now || !reflect.DeepEqual(got.Disks[failed].Reading, first.Disks[failed].Reading) {
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
			now = now.Add(30 * time.Minute)
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

func TestDiskReadingSurvivesRestartWithIndependentSizeAndUseAges(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	disks := Disks{DataDir: t.TempDir(), Now: func() time.Time { return now }}
	owner := NewDiskObserver(db, disks, disks.Now)
	if err := owner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := NewDiskObserver(db, disks, disks.Now)
	now = first.Add(30 * time.Minute)
	got, err := restarted.Snapshot(t.Context())
	if err != nil || got.Unknown || !got.Stale || len(got.Disks) != 1 || got.Disks[0].Reading.ReadAt != first {
		t.Fatalf("restart: %+v %v", got, err)
	}
	now = first.Add(6*time.Hour + time.Second)
	got, err = restarted.Snapshot(t.Context())
	if err != nil || !got.Unknown || got.Disks[0].Reading.TotalBytes == 0 {
		t.Fatalf("use expiry: %+v %v", got, err)
	}
	now = first.Add(7*24*time.Hour + time.Second)
	got, err = restarted.Snapshot(t.Context())
	if err != nil || got.Disks[0].Reading.TotalBytes != 0 {
		t.Fatalf("size expiry: %+v %v", got, err)
	}
}

func TestFirstWorkerDiskFailureRemainsRecordedAfterRecovery(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	db := databaseSuite.New(t)
	worker := &observedWorker{err: errors.New("unavailable")}
	disks := Disks{DataDir: t.TempDir(), Worker: worker, Now: func() time.Time { return now }}
	owner := NewDiskObserver(db, disks, disks.Now)
	if err := owner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed, err := owner.Snapshot(t.Context())
	if err != nil || failed.LastFailedAt != first || failed.LastFailure == "" || failed.Disks[1].FailedAt != first {
		t.Fatalf("first failure metadata missing: %+v %v", failed, err)
	}
	now = now.Add(5 * time.Minute)
	worker.err = nil
	worker.readings = []diskspace.Reading{{What: "worker state", ReadAt: now, TotalBytes: 100, FreeBytes: 50}}
	if err := owner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewDiskObserver(db, disks, disks.Now).Snapshot(t.Context())
	if err != nil || recovered.LastFailedAt != first || recovered.LastFailure == "" || recovered.Unknown {
		t.Fatalf("recovery erased last failed check: %+v %v", recovered, err)
	}
}

// A cooperative worker consumes only its own wait, never the entire job.
type waitingDiskWorker struct {
	readings []diskspace.Reading
	block    bool
}

func (w *waitingDiskWorker) Disks(ctx context.Context) ([]diskspace.Reading, error) {
	if !w.block {
		return w.readings, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDiskWorkerBoundAndOuterDeadlineKeepSuccessfulController(t *testing.T) {
	for _, outer := range []time.Duration{25 * time.Second, 100 * time.Millisecond} {
		t.Run(outer.String(), func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			first := now
			worker := &waitingDiskWorker{readings: []diskspace.Reading{diskspace.NewReading("worker state", "/state", diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 7 << 30}, nil, first)}}
			dir := t.TempDir()
			disks := Disks{DataDir: dir, Worker: worker, Now: func() time.Time { return now }}
			o := NewDiskObserver(databaseSuite.New(t), disks, disks.Now)
			if err := o.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			worker.block = true
			ctx, cancel := context.WithTimeout(t.Context(), outer)
			defer cancel()
			started := time.Now()
			err := o.Run(ctx)
			if outer > time.Second {
				if err != nil || time.Since(started) > 23*time.Second || ctx.Err() != nil {
					t.Fatalf("worker exhausted job: %v after %v", err, time.Since(started))
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost job deadline: %v", err)
			}
			got, err := o.Snapshot(t.Context())
			if err != nil || len(got.Disks) != 2 {
				t.Fatalf("snapshot: %+v %v", got, err)
			}
			if d := got.Disks[0]; d.Reading.ReadAt != now || !d.FailedAt.IsZero() {
				t.Fatalf("successful controller discarded: %+v", d)
			}
			if d := got.Disks[1]; d.Reading.ReadAt != first || d.FailedAt != now {
				t.Fatalf("worker evidence renewed or lost: %+v", d)
			}
		})
	}
}

func TestDiskMergeRetainsEmptyPathFailureAndReplacesSuccessfulPath(t *testing.T) {
	now := time.Now().UTC()
	first := now
	space := diskspace.Space{Filesystem: "6f515dedc9c260d3", TotalBytes: 100 << 30, FreeBytes: 7 << 30}
	state := diskspace.NewReading("the worker's state folder", "/state", space, nil, now)
	docker := diskspace.NewReading("the worker's Docker data (tasks, images, private Docker)", "/docker", space, nil, now)
	state.Categories = []diskspace.Category{{Name: "Workspaces", Bytes: 100}}
	docker.Categories = []diskspace.Category{{Name: "Images", Bytes: 200}}
	w := &observedWorker{readings: []diskspace.Reading{state, docker}}
	disks := Disks{DataDir: t.TempDir(), Worker: w, Now: func() time.Time { return now }}
	o := NewDiskObserver(databaseSuite.New(t), disks, disks.Now)
	for step := range 3 {
		if step == 1 {
			now = now.Add(time.Minute)
			w.readings[0].ReadAt = now
			w.readings[1] = diskspace.NewReading(docker.What, "", diskspace.Space{}, errors.New("Docker did not answer in time"), now)
		}
		if step == 2 {
			now = now.Add(time.Minute)
			w.readings[0].ReadAt = now
			docker.ReadAt = now
			docker.Path = "/new-docker"
			w.readings[1] = docker
		}
		if err := o.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		got, err := o.Snapshot(t.Context())
		if err != nil || len(got.Disks) != 3 {
			t.Fatalf("stored roles: %+v %v", got, err)
		}
		d := got.Disks[2]
		if step == 1 && (d.Reading.Path != "/docker" || d.Reading.ReadAt != first || d.Reading.FreeBytes != space.FreeBytes || d.Reading.Filesystem != space.Filesystem || len(d.Reading.Categories) != 1 || d.FailedAt != now || d.LastFailedAt != now || d.LastFailure == "") {
			t.Fatalf("failed probe erased evidence: %+v", d)
		}
		if step == 2 && (d.Reading.Path != "/new-docker" || d.Reading.ReadAt != now || !d.FailedAt.IsZero() || d.LastFailedAt.IsZero()) {
			t.Fatalf("recovery did not replace evidence: %+v", d)
		}
		rows := o.Read(t.Context())
		if len(rows) != 2 || rows[1].Unknown != "" || rows[1].ReadAt != now || len(rows[1].Categories) != 2 {
			t.Fatalf("step %d displayed duplicate or unknown disk: %+v", step, rows)
		}
	}
	// A real filesystem change is accepted on success and splits the display.
	now = now.Add(time.Minute)
	w.readings[1].Filesystem = "different"
	w.readings[1].ReadAt = now
	if err := o.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rows := o.Read(t.Context()); len(rows) != 3 {
		t.Fatalf("new filesystem retained old id: %+v", rows)
	}
}
