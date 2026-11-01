package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/ids"
)

// Pages read the same durable evidence after constructing a fresh service. A
// failed observation changes its diagnostic, never its successful figure times.
func TestCapacitySurvivesRestartAndExpiresEachFigureByWhatItMeasures(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	targets := NewTargets(db, nil)
	target, err := targets.Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{db: db, Targets: targets, clock: func() time.Time { return now }}
	observer := NewCapacityObserver(service)
	observer.read = func(context.Context, *Target) (TargetCapacity, error) {
		return TargetCapacity{Target: "host", Kind: KindDockerHost, ReadAt: now, Memory: &ResourceCapacity{Total: known(100), Promised: Promised{Sum: Split{AppTown: known(10), Other: known(20)}}, InUse: &Split{AppTown: known(10), Other: known(20)}}, Disk: &DiskCapacity{Total: known(100), Free: known(90), ReadAt: now}}, nil
	}
	if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	// A different service and observer share only the DB, never reading maps.
	restarted := &Service{db: db, Targets: NewTargets(db, nil), clock: func() time.Time { return now }}
	read := func() TargetCapacity {
		t.Helper()
		got, err := restarted.Capacity(t.Context(), target.Name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.Words == "Pending first check" || !got.Memory.Total.Known || !got.ReadAt.Equal(first) {
		t.Fatalf("restart lost reading: %+v", got)
	}
	now = first.Add(30 * time.Minute)
	if got := read(); !got.Stale || !got.Memory.InUse.AppTown.Known || got.Error != "" {
		t.Fatalf("idle reading: %+v", got)
	}
	observer.read = func(context.Context, *Target) (TargetCapacity, error) {
		return TargetCapacity{}, errors.New("host unreachable")
	}
	if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err == nil {
		t.Fatal("failed check reported success")
	}
	if got := read(); !got.Memory.Total.Known || !got.Memory.InUse.AppTown.Known || got.Error == "" || !got.Memory.InUse.AppTown.ReadAt.Equal(first) {
		t.Fatalf("failure lost prior figures: %+v", got)
	}
	now = first.Add(6*time.Hour + time.Second)
	if got := read(); got.Memory.InUse.AppTown.Known || got.Disk.Free.Known || !got.Memory.Total.Known || !got.Disk.Total.Known {
		t.Fatalf("live expiry contaminated size: %+v", got)
	}
	now = first.Add(7*24*time.Hour + time.Second)
	if got := read(); got.Memory.Total.Known || got.Disk.Total.Known {
		t.Fatalf("size never expired: %+v", got)
	}
}

// Retaining a display value must not turn it into admission evidence. Admission
// asks the daemon again and refuses a failure even with an old healthy reading.
func TestOldCapacityDiskCannotAdmitWorkWhenTheCurrentDiskIsFull(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	if _, err := backgroundCapacity(t, w.s, "lab"); err != nil {
		t.Fatal(err)
	}
	now := w.s.now().Add(30 * time.Minute)
	w.s.setClock(func() time.Time { return now })
	page, err := w.s.Capacity(t.Context(), "lab")
	if err != nil || page.Disk == nil || !page.Disk.Free.Known || !page.Stale {
		t.Fatalf("display reading: %+v %v", page, err)
	}
	w.hosts.daemon("lab").SetDisk(100<<30, 1<<20)
	scope, err := w.s.hosts.DockerScope(t.Context(), w.lab)
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.AdmitDisk(t.Context(), scope, w.s.env.ToolVersions().Images.DF); !errors.Is(err, diskspace.ErrRefused) {
		t.Fatalf("old display reading admitted work: %v", err)
	}
}

func TestInventoryReadingsSurviveRestartAndKeepFailedCheckEvidence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	target := &Target{ID: ids.New(now), Name: "host", SettingsRevision: 1}
	owner := NewInventoryReadings(db, func() time.Time { return now })
	generation, err := owner.Start(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Finish(t.Context(), target, generation, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	restarted := NewInventoryReadings(db, func() time.Time { return now })
	now = first.Add(30 * time.Minute)
	got, err := restarted.SnapshotForTarget(t.Context(), target)
	if err != nil || got.Unknown || !got.Stale || !got.ObservedAt.Equal(first) {
		t.Fatalf("restart: %+v %v", got, err)
	}
	if err := owner.Finish(t.Context(), target, generation, nil, nil, nil, errors.New("unreachable")); err == nil {
		t.Fatal("failure lost")
	}
	got, err = restarted.SnapshotForTarget(t.Context(), target)
	if err != nil || got.Unknown || got.FailedAt != now || got.ObservedAt != first {
		t.Fatalf("failure: %+v %v", got, err)
	}
	now = first.Add(6*time.Hour + time.Second)
	got, err = restarted.SnapshotForTarget(t.Context(), target)
	if err != nil || !got.Unknown {
		t.Fatalf("expiry: %+v %v", got, err)
	}
}

// A new disk reading cannot renew the tenant inventory when listing containers
// failed, including an old complete empty list.
func TestPartialCapacityCheckCannotRenewTenantPresence(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tenants := range [][]Tenant{nil, {{Name: "app", Kind: TenantDeployment, ReadAt: at, Containers: 1, Used: Figure{Known: true, Value: 10, ReadAt: at}, Promised: Figure{Known: true, Value: 20, ReadAt: at}}}} {
		old := TargetCapacity{ReadAt: at, TenantsAt: at, Tenants: tenants}
		now := at.Add(6*time.Hour + time.Second)
		partial := mergeCapacity(old, TargetCapacity{ReadAt: now, Memory: &ResourceCapacity{Total: known(100)}, Disk: &DiskCapacity{ReadAt: now, Free: known(90), Total: known(100)}}, now)
		got := capacityAt(partial, now)
		if !got.LiveUnknown || !got.Stale || got.TenantsAt != at || len(got.Tenants) != len(tenants) {
			t.Fatalf("partial check renewed tenant list: %+v", got)
		}
	}
}

func TestFailedElasticPriceCheckRetainsPricesForTheSameSize(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	good := mergeCapacity(TargetCapacity{}, TargetCapacity{ReadAt: at, Elastic: true, Parts: []ElasticPart{{Deployment: "app/main", Part: "web", RunsOn: "small", USDPerMonth: known(10)}}, Estimate: figurePointer(known(10))}, at)
	later := at.Add(30 * time.Minute)
	bad := TargetCapacity{ReadAt: later, Elastic: true, Error: "The capacity price check failed.", Parts: []ElasticPart{{Deployment: "app/main", Part: "web", RunsOn: "small", USDPerMonth: unknown("provider unavailable")}}, Estimate: figurePointer(unknown("provider unavailable"))}
	got := capacityAt(mergeCapacity(good, bad, later), later)
	if capacityFailure(bad) == "" || !got.Estimate.Known || got.Estimate.Value != 10 || got.Estimate.ReadAt != at || !got.Parts[0].USDPerMonth.Known || !got.Parts[0].USDPerMonth.Stale {
		t.Fatalf("failed price check erased known sizes/prices: %+v", got)
	}
	bad.Parts[0].RunsOn = "large"
	changed := mergeCapacity(good, bad, later)
	if changed.Parts[0].USDPerMonth.Known {
		t.Fatal("price reused for a different size")
	}
}
func figurePointer(f Figure) *Figure { return &f }

func TestElasticPriceFailureKeepsTheStoredCapacityPageAfterRestart(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	first := now
	targets := NewTargets(db, nil)
	target, err := targets.Put(t.Context(), &Target{Name: "apps", Kind: KindDOApp, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, Targets: targets, clock: func() time.Time { return now }}
	observer := NewCapacityObserver(s)
	price := known(10)
	reason := ""
	observer.read = func(context.Context, *Target) (TargetCapacity, error) {
		return TargetCapacity{Target: "apps", Kind: KindDOApp, Elastic: true, ReadAt: now, Error: reason, Parts: []ElasticPart{{Deployment: "app/main", Part: "web", RunsOn: "small", USDPerMonth: price}}, Estimate: figurePointer(price)}, nil
	}
	if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	price = unknown("provider unavailable")
	reason = "The capacity price check failed."
	if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{db: db, Targets: targets, clock: s.clock}
	got, err := restarted.Capacity(t.Context(), "apps")
	if err != nil || got.Words == "Pending first check" || got.Estimate == nil || !got.Estimate.Known || got.Estimate.Value != 10 || got.Estimate.ReadAt != first || got.Error != reason || !got.Parts[0].USDPerMonth.Stale {
		t.Fatalf("price failure hid the stored capacity page: %+v %v", got, err)
	}
}

// An offline Docker scope reports unread figures without a Go error. Its
// attempt time must never replace the last successful capacity reading.
func TestOfflineCapacityKeepsSuccessfulTimeAndHostMemoryUse(t *testing.T) {
	for _, origin := range []string{OriginAttached, OriginBuiltIn} {
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			first := now
			targets := NewTargets(db, nil)
			target, err := targets.Put(t.Context(), &Target{Name: "host", Kind: KindDockerHost, Origin: origin, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{db: db, Targets: targets, clock: func() time.Time { return now }}
			observer := NewCapacityObserver(service)
			observer.read = func(context.Context, *Target) (TargetCapacity, error) {
				return TargetCapacity{ReadAt: now, CPU: &ResourceCapacity{Total: known(4), Promised: Promised{Sum: Split{AppTown: known(1), Other: known(0)}}}, Memory: &ResourceCapacity{Total: known(100), HostUsed: figurePointer(known(40)), Promised: Promised{Sum: Split{AppTown: known(10), Other: known(20)}}}, Disk: &DiskCapacity{ReadAt: now, Total: known(100), Free: known(90)}}, nil
			}
			if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err != nil {
				t.Fatal(err)
			}
			observer.read = func(context.Context, *Target) (TargetCapacity, error) {
				return TargetCapacity{ReadAt: now, Memory: &ResourceCapacity{}, CPU: &ResourceCapacity{}, Disk: &DiskCapacity{}}, nil
			}
			for _, age := range []time.Duration{30 * time.Minute, 6*time.Hour + time.Second} {
				now = first.Add(age)
				if err := observer.Run(t.Context(), target.ID, target.SettingsRevision); err != nil {
					t.Fatal(err)
				}
				got, err := service.Capacity(t.Context(), target.Name)
				if err != nil {
					t.Fatal(err)
				}
				if !got.ReadAt.Equal(first) {
					t.Errorf("failed check renewed page time: %v, want %v", got.ReadAt, first)
				}
				if got.Memory.HostUsed == nil || got.Memory.HostUsed.Value != 40 || got.Memory.HostUsed.ReadAt != first {
					t.Errorf("failed check lost host memory use: %+v", got.Memory.HostUsed)
				}
				snapshot, err := observer.Snapshot(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.ObservedAt != first || snapshot.FailedAt != now || snapshot.Reason == "" {
					t.Errorf("failed check evidence: %+v", snapshot)
				}
				if snapshot.Unknown != (age > 6*time.Hour) {
					t.Errorf("wrong capacity expiry: %+v", snapshot)
				}
			}
		})
	}
}
