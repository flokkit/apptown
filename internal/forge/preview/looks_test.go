package preview

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids/idstest"
)

// forgeClock is a forge clock a test moves.
type forgeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newForgeClock() *forgeClock {
	return &forgeClock{now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}
func (c *forgeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *forgeClock) set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }

// lookAt is the preview's last look as Preview reads it, zero for none.
func lookAt(t *testing.T, s *Service, name string) time.Time {
	t.Helper()
	looks, err := s.lastLooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return looks[name]
}

// A look is written at most once a minute per preview, and never moves
// backwards.
func TestALookIsWrittenAtMostOnceAMinuteAndNeverBackwards(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	name, t0 := docker.PreviewChangeName(idstest.N(7)), clock.Now()
	s.Viewed(context.Background(), name)
	clock.set(t0.Add(30 * time.Second))
	s.Viewed(context.Background(), name)
	if got := lookAt(t, s, name); !got.Equal(t0) {
		t.Fatalf("a look within the minute was written: %v", got)
	}
	clock.set(t0.Add(61 * time.Second))
	s.Viewed(context.Background(), name)
	if got := lookAt(t, s, name); !got.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("a look a minute on was not written: %v", got)
	}
	clock.set(t0.Add(-time.Hour))
	s.Viewed(context.Background(), name)
	if got := lookAt(t, s, name); !got.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("a look moved backwards: %v", got)
	}
}

// A preview on this forge's Docker stops, whole, about eight hours after
// the last look at it: a boot is a look, a request in between postpones
// the stop, and the stop forgets its placement row and keeps its build.
func TestALocalPreviewStopsEightHoursAfterItsLastLook(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	writePreviewBootFixture(t, filepath.Join(w.dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	t0, name := clock.Now(), docker.PreviewChangeName(idstest.N(7))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: w.projectID}, false); err != nil {
		t.Fatal(err)
	}
	clock.set(t0.Add(7 * time.Hour))
	s.Tend(context.Background(), false)
	if !w.client.Running(name) {
		t.Fatal("a preview looked at seven hours ago was stopped")
	}
	s.Viewed(context.Background(), name)
	clock.set(t0.Add(9 * time.Hour))
	s.Tend(context.Background(), false)
	if !w.client.Running(name) {
		t.Fatal("a preview looked at two hours ago was stopped")
	}
	w.client.ResetCommands()
	clock.set(t0.Add(15*time.Hour + time.Minute))
	s.Tend(context.Background(), false)
	if w.client.Running(name) {
		t.Fatal("a preview nobody looked at for eight hours still runs")
	}
	requireNothingOfPreview(t, w.client, idstest.N(7))
	if _, err := s.deployments.ByName(context.Background(), w.projectID, deployment.PreviewName(idstest.N(7))); err == nil {
		t.Fatal("a stopped preview's placement row was kept")
	}
	if len(w.client.Matching("rmi ")) != 0 {
		t.Fatal("stopping a preview removed its build")
	}
}

// An app that exited is still there, with its services: it is stopped
// whole like any other.
func TestAnExitedPreviewIsStoppedWhole(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	writePreviewBootFixture(t, filepath.Join(w.dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: w.projectID}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.client.Run(context.Background(), "", docker.Command{Args: []string{"stop", docker.PreviewChangeName(idstest.N(7))}}); err != nil {
		t.Fatal(err)
	}
	w.client.ResetCommands()
	clock.set(clock.Now().Add(9 * time.Hour))
	s.Tend(context.Background(), false)
	requireNothingOfPreview(t, w.client, idstest.N(7))
}

// A boot holding a preview is a look: the stop leaves it. Looks that
// cannot be read stop nothing.
func TestTendingLeavesABootingPreviewAndStopsNothingItCannotJudge(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	writePreviewBootFixture(t, filepath.Join(w.dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	name := docker.PreviewChangeName(idstest.N(7))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: w.projectID}, false); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.Now().Add(9 * time.Hour))
	release := s.holdPreview(name)
	s.Tend(context.Background(), false)
	release()
	if !w.client.Running(name) {
		t.Fatal("a preview a boot holds was stopped")
	}
	dbtest.FailNextStatement(t, w.db, "-- name: PreviewViews")
	s.Tend(context.Background(), false)
	if !w.client.Running(name) {
		t.Fatal("a preview was stopped though its looks could not be read")
	}
}

// The look read when the pass began is a candidate only: one written
// since keeps the preview running.
func TestALookAfterTheCandidateWasChosenKeepsThePreview(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	writePreviewBootFixture(t, filepath.Join(w.dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	name := docker.PreviewChangeName(idstest.N(7))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: w.projectID}, false); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.Now().Add(9 * time.Hour))
	chosen, err := s.lastLooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Viewed(context.Background(), name)
	s.stopIdle(context.Background(), name, place{stack: docker.ChangeStackID(idstest.N(7))}, chosen, false)
	if !w.client.Running(name) {
		t.Fatal("a preview looked at after it was chosen was stopped")
	}
}

// On a worker the same rule holds: the forge stops the preview there, and
// keeps no record of it; its build stays.
func TestAWorkerPreviewStopsEightHoursAfterItsLastLook(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s, f, _, dir, pid := onWorker(t, w)
	s.clock, s.runtime.Now = clock.Now, clock.Now
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	name := docker.PreviewChangeName(idstest.N(9))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: pid}, false); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.Now().Add(7 * time.Hour))
	s.Tend(context.Background(), false)
	if !f.holds(name) {
		t.Fatal("a worker preview looked at seven hours ago was stopped")
	}
	clock.set(clock.Now().Add(2 * time.Hour))
	s.Tend(context.Background(), false)
	if f.holds(name) {
		t.Fatal("a worker preview nobody looked at for eight hours is still there")
	}
	if _, ok := s.servedRecordOf(name); ok {
		t.Fatal("the forge kept a record of a stopped preview")
	}
	if len(f.images) != 0 {
		t.Fatalf("stopping a worker preview removed its build: %v", f.images)
	}
}

// A release looked at on this forge's Docker beside a worker (a release
// recorded before bundles) is tended where it is.
func TestALocalReleaseLookBesideAWorkerIsStoppedWhereItIs(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s, _, _, _, _ := onWorker(t, w)
	s.clock = clock.Now
	name, stack := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", 3)), docker.ReleaseStackID("p", 3)
	if _, err := w.client.Run(context.Background(), "", docker.Command{Args: []string{"run", "-d", "--name", name,
		"--label", "forge.class=preview", "--label", docker.StackLabel + "=" + stack, "img"}}); err != nil {
		t.Fatal(err)
	}
	s.Tend(context.Background(), false)
	calls := previewCommands(w.client)
	for _, want := range []string{"rm -f -v " + name, "network rm " + docker.NetworkName(stack)} {
		if !slices.Contains(calls, want) {
			t.Fatalf("%q not issued: %v", want, calls)
		}
	}
}

// keptBuildWorld is a local preview of change 7 booted at the clock's time,
// whose daemon names its kept build image, with a snapshot folder of it.
func keptBuildWorld(t *testing.T) (*Service, *world, *forgeClock, string) {
	t.Helper()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	name := docker.PreviewChangeName(idstest.N(7))
	w.client.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "image" && args[1] == "ls" && slices.Contains(args, "reference="+docker.BuildTag(name)) {
			return []byte(docker.BuildTag(name) + ":latest\n"), nil, true
		}
		return nil, nil, false
	})
	writePreviewBootFixture(t, filepath.Join(w.dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: w.projectID}, false); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(s.changeSnapshots(idstest.N(7)), "abc")
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	return s, w, clock, snapshot
}

// A change's preview keeps its build image and snapshot thirty days after
// the last look, then they go, and the look with them.
func TestAChangesBuildIsKeptThirtyDaysAfterItsLastLook(t *testing.T) {
	t.Parallel()
	s, w, clock, snapshot := keptBuildWorld(t)
	name, t0 := docker.PreviewChangeName(idstest.N(7)), clock.Now()
	clock.set(t0.Add(29 * 24 * time.Hour))
	s.Tend(context.Background(), false) // stops it
	s.Tend(context.Background(), false)
	if _, err := os.Stat(snapshot); err != nil || len(w.client.Matching("rmi ")) != 0 || lookAt(t, s, name).IsZero() {
		t.Fatalf("a build looked at 29 days ago went: snapshot %v, removals %v", err, w.client.Matching("rmi "))
	}
	clock.set(t0.Add(31 * 24 * time.Hour))
	s.Tend(context.Background(), false)
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("a snapshot not looked at for 31 days stayed: %v", err)
	}
	if got := w.client.Matching("rmi "); len(got) != 1 || strings.Join(got[0], " ") != "rmi "+docker.BuildTag(name)+":latest" {
		t.Fatalf("its build image: %v", got)
	}
	if !lookAt(t, s, name).IsZero() {
		t.Fatal("the look outlived what it kept")
	}
}

// The expiry's candidate is checked again under the preview's lock: a look
// since, or the preview present again, keeps everything.
func TestAnExpiryCandidateLookedAtOrPresentSinceKeepsItsBuild(t *testing.T) {
	t.Parallel()
	s, w, clock, snapshot := keptBuildWorld(t)
	name := docker.PreviewChangeName(idstest.N(7))
	clock.set(clock.Now().Add(31 * 24 * time.Hour))
	// Still present: never stopped.
	s.expireKept(context.Background(), name, clock.Now().Add(-keepBuild), false)
	if _, err := os.Stat(snapshot); err != nil || len(w.client.Matching("rmi ")) != 0 {
		t.Fatal("a present preview's build was removed")
	}
	if err := s.DiscardChangePreview(context.Background(), idstest.N(7)); err != nil {
		t.Fatal(err)
	}
	cutoff := clock.Now().Add(-keepBuild) // chosen when the look was old
	s.Viewed(context.Background(), name)  // a reopen's look, since
	s.expireKept(context.Background(), name, cutoff, false)
	if _, err := os.Stat(snapshot); err != nil || len(w.client.Matching("rmi ")) != 0 || lookAt(t, s, name).IsZero() {
		t.Fatal("a build looked at since it was chosen was removed")
	}
}

// What a change's preview kept before the forge recorded looks goes; one
// present stays for its stop.
func TestABuildWithNoLookGoesUnlessItsPreviewIsThere(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s := w.service(Dependencies{Now: clock.Now})
	gone, there := filepath.Join(s.changeSnapshots(idstest.N(5)), "abc"), filepath.Join(s.changeSnapshots(idstest.N(6)), "abc")
	for _, dir := range []string{gone, there} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.client.Run(context.Background(), "", docker.Command{Args: []string{"run", "-d", "--name", docker.PreviewChangeName(idstest.N(6)), "--label", "forge.class=preview", "img"}}); err != nil {
		t.Fatal(err)
	}
	s.Tend(context.Background(), false)
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("a snapshot with no look and no preview stayed: %v", err)
	}
	if _, err := os.Stat(there); err != nil {
		t.Fatalf("a snapshot of a preview that was there went: %v", err)
	}
}

// On a worker the kept build images go there, through the worker, and the
// snapshot on the forge.
func TestAWorkerPreviewsBuildGoesThroughTheWorker(t *testing.T) {
	t.Parallel()
	clock := newForgeClock()
	w := newWorld(t, "", "")
	s, f, _, dir, pid := onWorker(t, w)
	s.clock, s.runtime.Now = clock.Now, clock.Now
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	name := docker.PreviewChangeName(idstest.N(9))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: pid}, false); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(s.changeSnapshots(idstest.N(9)), "abc")
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.Now().Add(29 * 24 * time.Hour))
	s.Tend(context.Background(), false)
	s.Tend(context.Background(), false)
	if len(f.images) != 0 {
		t.Fatalf("a build looked at 29 days ago went: %v", f.images)
	}
	clock.set(clock.Now().Add(2 * 24 * time.Hour))
	s.Tend(context.Background(), false)
	if !slices.Equal(f.images, []string{name}) {
		t.Fatalf("the worker was asked to remove %v", f.images)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("the snapshot stayed: %v", err)
	}
}

// A deleted project's preview goes for good: on the worker its stack and
// the build it kept, and its look; nothing stays for a reopen.
func TestRemovingAPreviewForGoodTakesItsBuildAndItsLook(t *testing.T) {
	t.Parallel()
	s, f, _, dir, pid := workerPreviewWorld(t)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	name := docker.PreviewChangeName(idstest.N(9))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: pid}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePreviewForGood(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if f.holds(name) || !slices.Equal(f.images, []string{name}) || !lookAt(t, s, name).IsZero() {
		t.Fatalf("left: preview %v, images removed %v, look %v", f.holds(name), f.images, lookAt(t, s, name))
	}
}
