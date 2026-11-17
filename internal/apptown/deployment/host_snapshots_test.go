package deployment

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type failedStandingReader struct {
	daemon *dockerfake.Daemon
	fail   bool
}

func (f *failedStandingReader) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	if f.fail && c.Args[0] == "exec" && strings.Contains(c.Args[1], "disk-reader") {
		return nil, errors.New("reader unavailable")
	}
	return f.daemon.Run(ctx, host, c)
}

// Runtime evidence advances even when the independently read disk fails.
func TestHostSnapshotsPreserveEachSectionsEvidenceTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	first := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	now := first
	targets := NewTargets(db, nil)
	target, err := targets.Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	s := NewHostSnapshots(db, func() time.Time { return now })
	d := dockerfake.New()
	d.SetRunning("apptown-dep-one", true)
	reader := &failedStandingReader{daemon: d}
	dk := docker.Scope{Client: reader, Now: func() time.Time { return now }}
	q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, Use: true, ReaderImage: "df:test"}
	if _, _, err = s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	now = first.Add(20 * time.Minute)
	if _, err := d.Run(t.Context(), "", docker.Command{Args: []string{"stop", "apptown-dep-one"}}); err != nil {
		t.Fatal(err)
	}
	reader.fail = true
	if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	// Partial evidence keeps the whole-reading mark, even at the same clock.
	if _, _, err = s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	// Construct a new owner to prove persistence rather than an in-memory merge.
	got, err := NewHostSnapshots(db, func() time.Time { return now }).reading(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "partial" || got.Use.Value == nil || !got.Use.ObservedAt.Equal(first) || got.Use.Reason == "" {
		t.Fatalf("disk evidence renewed or lost: %+v", got)
	}
	if !got.Containers.ObservedAt.Equal(now) || (*got.Containers.Value)[0].State.Running {
		t.Fatalf("runtime held back by failed disk read: %+v", got.Containers)
	}
	if now.Sub(got.Use.ObservedAt) <= liveFresh {
		t.Fatal("retained disk was made fresh")
	}
	if !got.Invalidated {
		t.Fatal("old use lost its act mark")
	}
	inv, err := s.inventory(got)
	if err != nil || inv.Unknown || !inv.Invalidated || len(inv.Items) != 1 || inv.Items[0].Status != "stopped" || !inv.ObservedAt.Equal(now) {
		t.Fatalf("partial lap lost whole-reading mark: %+v %v", inv, err)
	}

}

func TestHostSnapshotsFirstPartialReadingDoesNotInventAbsence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Now()
	target, err := NewTargets(db, nil).Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	s := NewHostSnapshots(db, func() time.Time { return now })
	d := dockerfake.New()
	d.SetRunning("apptown-dep-one", true)
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "volume" {
			return nil, errors.New("volume listing failed"), true
		}
		return nil, nil, false
	})
	if _, _, err = s.collect(t.Context(), target, docker.Scope{Client: d}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	got, err := s.reading(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if got.Containers.Value == nil || len(*got.Containers.Value) != 1 || got.Resources.Value != nil || !got.Resources.ObservedAt.IsZero() {
		t.Fatalf("partial reading: %+v", got)
	}
	inv, err := s.inventory(got)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Unknown || len(inv.Missing) != 0 {
		t.Fatalf("unread resources became absent: %+v", inv)
	}
}

func TestHostSnapshotsActInvalidationAndSettingsIdentity(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Now()
	targets := NewTargets(db, nil)
	target, err := targets.Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	s := NewHostSnapshots(db, func() time.Time { return now })
	d := dockerfake.New()
	q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, Use: true, ReaderImage: "df:test"}
	if _, err = s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.reading(t.Context(), target)
	if err != nil || !r.Invalidated || r.Host.Value != nil {
		t.Fatalf("act before first read: %+v %v", r, err)
	}
	if _, _, err = s.collect(t.Context(), target, docker.Scope{Client: d}, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	r, err = s.reading(t.Context(), target)
	if err != nil || !r.Invalidated || r.Host.Value == nil {
		t.Fatalf("act erased evidence: %+v %v", r, err)
	}
	if _, _, err = s.collect(t.Context(), target, docker.Scope{Client: d}, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	r, err = s.reading(t.Context(), target)
	if err != nil || r.Invalidated {
		t.Fatalf("new collection stayed invalidated: %+v %v", r, err)
	}
	if r.Images.Value == nil || len(*r.Images.Value) != 0 || r.Containers.Value == nil || len(*r.Containers.Value) != 0 {
		t.Fatalf("successful empty sections became unread after persistence: %+v", r)
	}
	target.SettingsRevision++
	r, err = s.reading(t.Context(), target)
	if err != nil || r.Host.Value != nil {
		t.Fatalf("reconfigured target inherited facts: %+v %v", r, err)
	}
}

// An act concurrent with I/O cannot be cleared by the older collection.
func TestHostSnapshotsActDuringCollectionKeepsInvalidation(t *testing.T) {
	t.Parallel()
	for _, elapsed := range []time.Duration{0, time.Nanosecond, time.Microsecond} {
		t.Run(elapsed.String(), func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			// Include sub-microsecond precision: PostgreSQL stores the act as
			// timestamptz, while section evidence retains JSON nanoseconds.
			first := time.Date(2026, 11, 2, 0, 0, 0, 29, time.UTC)
			now := first
			target, err := NewTargets(db, nil).Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			s := NewHostSnapshots(db, func() time.Time { return now })
			d := dockerfake.New()
			dk := docker.Scope{Client: d, Now: s.now}
			q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}
			if _, _, err := s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
				t.Fatal(err)
			}
			acted := first.Add(elapsed)
			d.SetReply(func(args []string) ([]byte, error, bool) {
				if args[0] == "volume" {
					now = acted
					if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
						t.Error(err)
					}
				}
				return nil, nil, false
			})
			if _, _, err := s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
				t.Fatal(err)
			}
			r, err := s.reading(t.Context(), target)
			if err != nil || !r.Invalidated {
				t.Fatalf("overlapping read lost act: %+v %v", r, err)
			}
			inv, err := s.inventory(r)
			if err != nil || !inv.Invalidated || !inv.ObservedAt.Equal(first) {
				t.Fatalf("overlapping read renewed evidence: %+v %v", inv, err)
			}
			d.SetReply(nil)
			// A new complete collection clears the generation fence even if
			// the clock has not advanced since the act.
			if _, _, err := s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
				t.Fatal(err)
			}
			r, err = s.reading(t.Context(), target)
			if err != nil || r.Invalidated {
				t.Fatalf("post-act reading: %+v %v", r, err)
			}
			inv, err = s.inventory(r)
			if err != nil || inv.Invalidated || !inv.ObservedAt.Equal(acted) {
				t.Fatalf("post-act inventory: %+v %v", inv, err)
			}
		})
	}
}

// Missing claims require records and provider evidence from the same collection.
// Stale evidence retains a witnessed absence, and so does a reading an act
// has made out of date: it stays, marked invalidated, until refreshed.
func TestHostSnapshotsMissingClaimsUseCollectedRecords(t *testing.T) {
	t.Parallel()
	svc, db, daemon, projectID, target := healthWorld(t)
	first := time.Now()
	now := first
	s := NewHostSnapshots(db, func() time.Time { return now })
	cache := NewInventoryReadings(db, func() time.Time { return now })
	if err := svc.Deployments.RecordPreview(t.Context(), projectID, idstest.N(7), target.ID, docker.PreviewChangeName(idstest.N(7)), 40001, "http://preview", nil); err != nil {
		t.Fatal(err)
	}
	check := func(stage string, missing int, stale, unknown, invalidated bool) {
		t.Helper()
		r, err := NewHostSnapshots(db, func() time.Time { return now }).reading(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.inventory(r)
		if err != nil {
			t.Fatal(err)
		}
		old, err := cache.snapshotForTarget(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Missing) != missing || got.Stale != stale || got.Unknown != unknown || got.Invalidated != invalidated {
			t.Fatalf("%s: snapshot %+v", stage, got)
		}
		if got.Stale != old.Stale || got.Unknown != old.Unknown || got.Invalidated != old.Invalidated {
			t.Fatalf("%s: classifications differ: snapshot %+v, cache %+v", stage, got, old)
		}
		// Both keep prior missing claims after an act, marked invalidated:
		// they were witnessed by a complete collection.
		if !reflect.DeepEqual(got.Missing, old.Missing) {
			t.Fatalf("%s: missing differs: snapshot %+v, cache %+v", stage, got.Missing, old.Missing)
		}
	}
	check("never read", 0, true, true, false)
	collect := func() {
		t.Helper()
		if _, _, err := s.collect(t.Context(), target, docker.Scope{Client: daemon, Now: func() time.Time { return now }}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, svc.Inventory, s.now().Add(hostReadInterval)); err != nil {
			t.Fatal(err)
		}
		r, err := s.reading(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := svc.Inventory.Recorded(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := cache.Start(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		if err := cache.Finish(t.Context(), target, generation, docker.HostInventory(r.HostFacts, r.Instance, r.Also), nil, recorded, nil); err != nil {
			t.Fatal(err)
		}
	}
	collect()
	check("fresh", 1, false, false, false)
	now = first.Add(liveFresh + time.Second)
	check("stale but known", 1, true, false, false)
	if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	if err := cache.Invalidate(t.Context(), target.Name); err != nil {
		t.Fatal(err)
	}
	check("after act", 1, true, false, true)
	collect()
	check("refreshed after act", 1, false, false, false)
	// A preview recorded after collection was never compared with these facts.
	if err := svc.Deployments.RecordPreview(t.Context(), projectID, idstest.N(8), target.ID, docker.PreviewChangeName(idstest.N(8)), 40002, "http://second", nil); err != nil {
		t.Fatal(err)
	}
	check("new preview", 1, false, false, false)
}

// Capacity samples belong to the lap, on the five-minute cadence and after
// acts. Inventory-only laps cannot renew retained use or capacity timestamps.
func TestHostReaderSamplesCapacityOnlyWhenDue(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	svc.setClock(func() time.Time { return now })
	snapshots := NewHostSnapshots(db, svc.now)
	observer := NewCapacityObserver(svc)
	worker := HostReaders{Snapshots: snapshots, Targets: svc.Targets, Hosts: svc.hosts, Providers: svc.providers, Inventory: svc.Inventory, Capacity: observer}
	lap := func() {
		t.Helper()
		if err := worker.lap(t.Context(), target.ID, now.Add(hostReadInterval)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"run", "--name", "running", "--memory", "1048576", "--cpus", "0.5", "app:test"},
		{"run", "--name", "stopped", "--memory", "1073741824", "--cpus", "8", "app:test"},
		{"stop", "stopped"},
	} {
		if _, e := daemon.Run(t.Context(), "", docker.Command{Args: args}); e != nil {
			t.Fatal(e)
		}
	}
	lap()
	first, err := snapshots.reading(t.Context(), target)
	if err != nil || first.Use.Value == nil {
		t.Fatalf("first lap did not sample use: %+v %v", first, err)
	}
	capacity, err := observer.load(t.Context(), target)
	if err != nil || capacity.AttemptedAt.IsZero() || capacity.Capacity.Memory == nil {
		t.Fatalf("lap did not publish capacity: %+v %v", capacity, err)
	}
	if capacity.Capacity.Memory.Promised.Sum.Other.Value != 1048576 || capacity.Capacity.CPU.Promised.Sum.Other.Value != 0.5 {
		t.Fatalf("stopped containers counted as promises: %+v %+v", capacity.Capacity.Memory.Promised, capacity.Capacity.CPU.Promised)
	}
	for _, age := range []time.Duration{20 * time.Second, capacityInterval - time.Nanosecond} {
		now = first.AttemptedAt.Add(age)
		before := len(daemon.Commands())
		lap()
		r, e := snapshots.reading(t.Context(), target)
		c, ce := observer.load(t.Context(), target)
		if e != nil || ce != nil || !r.Use.ObservedAt.Equal(first.Use.ObservedAt) || !c.AttemptedAt.Equal(capacity.AttemptedAt) || !reflect.DeepEqual(c.Capacity, capacity.Capacity) {
			t.Fatalf("intervening lap renewed use: %+v %+v %v %v", r.Use, c, e, ce)
		}
		for _, cmd := range daemon.Commands()[before:] {
			if cmd[0] == "stats" || cmd[0] == "exec" {
				t.Fatalf("intervening lap sampled use: %v", cmd)
			}
		}
	}
	now = first.AttemptedAt.Add(capacityInterval)
	lap()
	c, err := observer.load(t.Context(), target)
	if err != nil || !c.AttemptedAt.Equal(now) {
		t.Fatalf("five-minute sample omitted: %+v %v", c, err)
	}
	now = now.Add(hostReadInterval)
	done, err := snapshots.BeforeAct(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	lap()
	c, err = observer.load(t.Context(), target)
	if err != nil || !c.AttemptedAt.Equal(now) {
		t.Fatalf("act did not sample: %+v %v", c, err)
	}
	// An act ending after a sample must be seen by the following lap too.
	done()
	now = now.Add(hostReadInterval)
	lap()
	c, err = observer.load(t.Context(), target)
	if err != nil || !c.AttemptedAt.Equal(now) {
		t.Fatalf("act completion lost: %+v %v", c, err)
	}
}

// Every passive page read uses the current counter; neither newer section times
// nor a partial lap can certify that a reading follows an act.
func TestHostReadingActProvenanceReachesInventoryPages(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	first := time.Date(2026, 11, 3, 0, 0, 0, 29, time.UTC)
	now := first
	svc.setClock(func() time.Time { return now })
	s := NewHostSnapshots(db, func() time.Time { return now })
	dk := docker.Scope{Client: daemon, Now: s.now}
	query := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}
	daemon.SetRunning("apptown-dep-one", true)
	lap := func() {
		t.Helper()
		if _, _, err := s.collect(t.Context(), target, dk, query, nil, nil, s.now().Add(hostReadInterval)); err != nil {
			t.Fatal(err)
		}
	}
	page := func(want bool) {
		t.Helper()
		got, err := svc.InventoryFor(t.Context(), target)
		if err != nil || got.Invalidated != want || got.Unknown {
			t.Fatalf("inventory page: %+v %v; want invalidated %v", got, err, want)
		}
		if live := svc.InventoryLive(t.Context(), target); live.Invalidated != want {
			t.Fatalf("page lost act mark: %v, want %v", live.Invalidated, want)
		}
	}
	lap()
	page(false)
	if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	page(true) // Before another lap, with exactly the same clock.
	now = now.Add(time.Minute)
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "ps" {
			return nil, errors.New("container read failed"), true
		}
		return nil, nil, false
	})
	lap()
	page(true)
	r, err := s.reading(t.Context(), target)
	if err != nil || !r.Containers.ObservedAt.Equal(first) || !r.Host.ObservedAt.Equal(now) || r.Containers.Value == nil || r.Containers.Reason == "" {
		t.Fatalf("failed section lost evidence: %+v %v", r, err)
	}
	daemon.SetReply(nil)
	lap()
	page(false)
	// Capture the lap counter, then act before the first command finishes.
	// Every section has a later JSON timestamp, which cannot clear the mark.
	// Advance once before this lap, then again inside it. Even an older
	// captured counter must not advance complete-lap provenance in the write.
	before, err := deploymentsql.New(db.SQL()).Reading(t.Context(), deploymentsql.ReadingParams{Subject: target.ID.String(), Kind: hostReadingKind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	acted := false
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" && !acted {
			acted = true
			if _, err := s.BeforeAct(t.Context(), target.ID); err != nil {
				t.Error(err)
			}
			now = now.Add(time.Microsecond)
		}
		return nil, nil, false
	})
	lap()
	page(true)
	r, err = s.reading(t.Context(), target)
	if err != nil || !r.Containers.ObservedAt.Equal(now) || !r.Host.ObservedAt.Equal(now) {
		t.Fatalf("interrupted lap discarded its newer facts: %+v %v", r, err)
	}
	// sql-guarantee: provenance advances only when the storing write checks
	// the captured counter against the current counter, even across two acts.
	stored, err := deploymentsql.New(db.SQL()).Reading(t.Context(), deploymentsql.ReadingParams{Subject: target.ID.String(), Kind: hostReadingKind})
	if err != nil || stored.CompleteActCounter != before.CompleteActCounter {
		t.Fatalf("interrupted lap advanced complete provenance: before=%d after=%d %v", before.CompleteActCounter, stored.CompleteActCounter, err)
	}
	daemon.SetReply(nil)
	lap()
	page(false) // No clock change at all since the interrupted lap.
}

// A foreground read during the lap never publishes its smaller answer.
func TestForegroundHostReadDoesNotWriteStoredReading(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	s := NewHostSnapshots(db, func() time.Time { return now })
	dk := docker.Scope{Client: daemon, Now: s.now}
	q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}
	if _, _, err := s.collect(t.Context(), target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := dk
	blocked.Client = sshDockerFake(func(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
		if cmd.Args[0] == "ps" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return daemon.Run(ctx, host, cmd)
	})
	now = now.Add(time.Minute)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.collect(t.Context(), target, blocked, q, nil, svc.Inventory, s.now().Add(hostReadInterval))
		done <- err
	}()
	<-entered
	foreground, err := docker.ReadHost(t.Context(), dk, docker.HostQuery{Host: true})
	if err != nil || foreground.Host.Value == nil {
		t.Fatalf("foreground blocked or failed: %+v %v", foreground, err)
	}
	r, err := s.reading(t.Context(), target)
	if err != nil || !r.AttemptedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("foreground published a reading: %+v %v", r, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r, err = s.reading(t.Context(), target)
	if err != nil || !r.AttemptedAt.Equal(now) || r.Resources.Value == nil {
		t.Fatalf("lap did not publish its whole reading: %+v %v", r, err)
	}
}

func TestInventoryCarriesTheStoredLapsDuration(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	first := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	now := first
	svc.setClock(func() time.Time { return now })
	snapshots := NewHostSnapshots(db, svc.now)
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" {
			now = first.Add(3 * time.Second)
		}
		return nil, nil, false
	})
	if _, _, err := snapshots.collect(t.Context(), target, docker.Scope{Client: daemon, Now: svc.now}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil, snapshots.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	live := svc.InventoryLive(t.Context(), target)
	if live.Reason != "" || live.Unknown || !live.ObservedAt.Equal(now) || live.TookSeconds != 3 {
		t.Fatalf("stored source metadata: %+v", live)
	}
}

// A complete lap that runs in the middle of an act cannot leave the host
// reading current once the act ends: the act marks the host again.
func TestHostSnapshotsLapDuringActLeavesReadingInvalidated(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	target, err := NewTargets(db, nil).Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	s := NewHostSnapshots(db, nil)
	dk := docker.Scope{Client: dockerfake.New()}
	q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}
	r, _, in, _ := executionFixture()
	in.TargetID = target.ID
	r.BeforeAct = s.BeforeAct
	if _, err := r.Run(t.Context(), in, func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
		if _, _, err := s.collect(ctx, target, dk, q, nil, nil, s.now().Add(hostReadInterval)); err != nil {
			t.Fatal(err)
		}
		return activeExecution(ctx, at, checkpoint)
	}); err != nil {
		t.Fatal(err)
	}
	reading, err := s.reading(t.Context(), target)
	if err != nil || !reading.Invalidated {
		t.Fatalf("a lap begun during the act left the reading current after it: %+v %v", reading, err)
	}
}

func TestHostReadingDurationIncludesPersistenceBeforePublication(t *testing.T) {
	t.Parallel()
	_, db, daemon, _, target := healthWorld(t)
	began := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	now := began
	snapshots := NewHostSnapshots(db, func() time.Time { return now })
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" {
			now = now.Add(28 * time.Second)
		}
		return nil, nil, false
	})
	writes := 0
	stop := db.ObserveStatements("-- name: FinishHostSnapshot", func([]any) {
		writes++
		if writes == 1 {
			now = now.Add(9 * time.Second)
		}
	})
	defer stop()
	if _, _, err := snapshots.collect(t.Context(), target, docker.Scope{Client: daemon, Now: snapshots.now}, docker.HostQuery{Host: true, Containers: true}, nil, nil, began.Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	r, err := snapshots.reading(t.Context(), target)
	if err != nil || r.Took != 37*time.Second {
		t.Fatalf("publication omitted persistence time: %+v %v", r, err)
	}
	// The first write is uncommitted. If final publication fails, readers keep
	// the previous complete reading rather than seeing an intermediate duration.
	now = now.Add(time.Minute)
	restore := db.FailStatementAfter("-- name: FinishHostSnapshot", 1)
	defer restore()
	if _, _, err := snapshots.collect(t.Context(), target, docker.Scope{Client: daemon, Now: snapshots.now}, docker.HostQuery{Host: true, Containers: true}, nil, nil, now.Add(hostReadInterval)); err == nil {
		t.Fatal("publication failure was hidden")
	}
	kept, err := snapshots.reading(t.Context(), target)
	if err != nil || kept.AttemptedAt != r.AttemptedAt || kept.Took != r.Took {
		t.Fatalf("failed publication exposed staged duration: %+v %v", kept, err)
	}
}
