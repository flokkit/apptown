package deployment

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if err := s.Invalidate(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	// Fresh partial evidence must strictly follow the act at stored precision.
	now = now.Add(time.Microsecond)
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
	if !got.Invalidated || got.PredatesActAt.IsZero() {
		t.Fatal("old use lost its act mark")
	}
	inv, err := s.inventory(got)
	if err != nil || inv.Unknown || inv.Invalidated || len(inv.Items) != 1 || inv.Items[0].Status != "stopped" || !inv.ObservedAt.Equal(now) {
		t.Fatalf("failed disk withheld fresh inventory: %+v %v", inv, err)
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
	if err = s.Invalidate(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	r, err := s.reading(t.Context(), target)
	if err != nil || !r.Invalidated || r.Host.Value != nil {
		t.Fatalf("act before first read: %+v %v", r, err)
	}
	if err = s.collect(t.Context(), target, docker.Scope{Client: d}, q, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Invalidate(t.Context(), target); err != nil {
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
					if err := s.Invalidate(t.Context(), target); err != nil {
						t.Error(err)
					}
				}
				return nil, nil, false
			})
			if err := s.collect(t.Context(), target, dk, q, nil, nil); err != nil {
				t.Fatal(err)
			}
			r, err := s.reading(t.Context(), target)
			if err != nil || !r.Invalidated || !r.PredatesActAt.Equal(acted.Truncate(time.Microsecond)) {
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
			if err != nil || r.Invalidated || !r.PredatesActAt.IsZero() {
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
// Stale evidence retains a witnessed absence; an act withholds it until refreshed.
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
		// The old cache keeps prior missing claims after an act; snapshots
		// withhold them while the host facts predate that act.
		if !invalidated && !reflect.DeepEqual(got.Missing, old.Missing) {
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
	if err := s.Invalidate(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := cache.Invalidate(t.Context(), target.Name); err != nil {
		t.Fatal(err)
	}
	check("after act", 0, true, false, true)
	collect()
	check("refreshed after act", 1, false, false, false)
	// A preview recorded after collection was never compared with these facts.
	if err := svc.Deployments.RecordPreview(t.Context(), projectID, idstest.N(8), target.ID, docker.PreviewChangeName(idstest.N(8)), 40002, "http://second", nil); err != nil {
		t.Fatal(err)
	}
	check("new preview", 1, false, false, false)
}

// The inventory pass does not run the disk reader before use has a consumer.
func TestHostSnapshotWorkerCollectsOnlyInventorySections(t *testing.T) {
	t.Parallel()
	svc, db, daemon, _, target := healthWorld(t)
	s := NewHostSnapshots(db, nil)
	worker := HostSnapshotWorker{Snapshots: s, Targets: svc.Targets, Hosts: svc.hosts, Providers: svc.providers, Inventory: svc.Inventory}
	if _, err := worker.Run(t.Context(), target.ID.String()+"/"+strconv.FormatInt(target.SettingsRevision, 10), nil); err != nil {
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
