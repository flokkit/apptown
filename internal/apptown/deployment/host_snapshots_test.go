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
	if err = s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
	if err = s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
	if err = s.collect(t.Context(), target, docker.Scope{Client: d}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil); err != nil {
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
	if err = s.collect(t.Context(), target, docker.Scope{Client: d}, q, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeforeAct(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	r, err = s.reading(t.Context(), target)
	if err != nil || !r.Invalidated || r.Host.Value == nil {
		t.Fatalf("act erased evidence: %+v %v", r, err)
	}
	if err = s.collect(t.Context(), target, docker.Scope{Client: d}, q, nil, nil); err != nil {
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
			if err := s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
			if err := s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
			if err := s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
		if err := s.collect(t.Context(), target, docker.Scope{Client: daemon, Now: func() time.Time { return now }}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, svc.Inventory); err != nil {
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

// The inventory pass does not run the disk reader before use has a consumer.
func TestHostReaderCollectsOnlyInventorySections(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	s := NewHostSnapshots(db, nil)
	worker := HostReaders{Snapshots: s, Targets: svc.Targets, Hosts: svc.hosts, Providers: svc.providers, Inventory: svc.Inventory}
	if err := worker.lap(t.Context(), target.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.reading(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if r.Host.Value == nil || r.Containers.Value == nil || r.Resources.Value == nil || r.Images.Value == nil || r.Use.Value != nil || !r.Use.ObservedAt.IsZero() || r.Use.Reason != "" {
		t.Fatalf("inventory pass read wrong sections: %+v", r)
	}
	for _, c := range daemon.Commands() {
		if c[0] == "exec" || c[0] == "cp" || c[0] == "run" {
			t.Fatalf("inventory ran the disk reader: %v", daemon.Commands())
		}
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
		if err := s.collect(t.Context(), target, dk, query, nil, nil); err != nil {
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
	if err := s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
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
	go func() { done <- s.collect(t.Context(), target, blocked, q, nil, svc.Inventory) }()
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
	if err := snapshots.collect(t.Context(), target, docker.Scope{Client: daemon, Now: svc.now}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true}, nil, nil); err != nil {
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
		if err := s.collect(ctx, target, dk, q, nil, nil); err != nil {
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
