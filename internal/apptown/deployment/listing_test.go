package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

type listingWorld struct {
	err      error
	projects []ids.ID
}

func (w *listingWorld) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return w.projects, w.err
}

func (w *listingWorld) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: "alice"}, Verified: true}, w.err
}

type listingDriver struct {
	fakeDriver
	observe func(context.Context, deploy.Placement) (deploy.Observation, error)
}

func (d listingDriver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	return d.observe(ctx, p)
}
func listingFixture(t *testing.T, n int, observe func(context.Context, deploy.Placement) (deploy.Observation, error)) (*Service, *listingWorld, []*Deployment, ids.ID) {
	t.Helper()
	s, _, in, _ := configurationFixture(t)
	w := &listingWorld{projects: []ids.ID{in.ProjectID}}
	s.authority = w
	s.providers.drivers = map[string]DriverFactory{KindDOApp: func(context.Context, *Target, *Deployment) (deploy.Driver, error) {
		return listingDriver{observe: observe}, nil
	}}
	envs := make([]*Deployment, n)
	for i := range n {
		in.Name = fmt.Sprintf("app-%d", i)
		envs[i] = configureForTest(t, s, in)
	}
	return s, w, envs, in.ProjectID
}
func absentObservation(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
	return deploy.Observation{Generation: p.Generation, Rollout: "absent"}, nil
}

func TestListReturnsOrderedTypedFactsAndIndependentObservations(t *testing.T) {
	t.Parallel()
	s, _, envs, projectID := listingFixture(t, 3, func(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
		if p.Name == "app-1" {
			return deploy.Observation{}, errors.New("unavailable")
		}
		return deploy.Observation{Generation: p.Generation, Rollout: "active", URL: "https://observed/" + p.Name}, nil
	})
	for i, e := range envs {
		if err := s.Deployments.RecordAddress(t.Context(), e.ID, "https://recorded"); err != nil {
			t.Fatal(err)
		}
		if err := s.Needs.RecordIntent(context.Background(), e.ID, "postgres", map[string]any{"name": fmt.Sprintf("requested-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Needs.RecordFulfilled(context.Background(), envs[0].ID, "postgres", nil, map[string]any{"id": "db-1", "name": "actual", "region": "syd", "version": "16", "size": "small"}); err != nil {
		t.Fatal(err)
	}
	// Another project's records must not leak through the batched selectors.
	other := createTestProject(t, s.db, "other", "Other")
	outside := configureForTest(t, s, ConfigureDeployment{ProjectID: other, Name: "outside", Target: "cloud"})
	if err := s.Needs.RecordIntent(context.Background(), outside.ID, "redis", map[string]any{"name": "private"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(context.Background(), projectID)
	if err != nil || len(got) != 3 {
		t.Fatalf("listing: %+v %v", got, err)
	}
	for i, r := range got {
		if r.DeploymentPolicy != CompatibleUpdate || r.Name != envs[i].Name || r.Kind != Standing || r.Readiness != envs[i].Readiness || r.Target == nil || r.Target.Name != "cloud" || len(r.Needs) != 1 {
			t.Fatalf("row %d: %+v", i, r)
		}
		if r.Needs[0].Need != "postgres" {
			t.Fatalf("foreign service: %+v", r.Needs)
		}
	}
	if got[0].Observation.State != RuntimeRunning || got[0].URL != "https://observed/app-0" || got[0].Needs[0].Name != "actual" || got[0].Needs[0].Status != NeedReady || got[0].Needs[0].Version != "16" {
		t.Fatalf("observed: %+v", got[0])
	}
	if got[1].Observation.State != RuntimeUnknown || got[1].Observation.State.Running() != nil || got[1].Observation.Err == nil || got[1].URL != "https://recorded" || got[1].Needs[0].Name != "requested-1" {
		t.Fatalf("unknown: %+v", got[1])
	}
	runtime, err := s.Runtime(context.Background())
	if err != nil || len(runtime) != 3 || runtime[0].URL != "https://observed/app-0" {
		t.Fatalf("runtime display: %+v %v", runtime, err)
	}
	// Listing does not write live observations over persisted readiness or addresses.
	record, err := s.Deployments.ByID(context.Background(), envs[0].ID)
	if err != nil || record.URL != "https://recorded" || record.Readiness != envs[0].Readiness {
		t.Fatalf("listing mutated record: %+v %v", record, err)
	}
}

func TestListFailsStoredFactsAndAuthorityBeforeProviderCalls(t *testing.T) {
	for _, scenario := range []string{"authority", "cancelled", "snapshot", "deployments", "targets", "needs", "malformed-need", "malformed-deployment"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			s, w, envs, projectID := listingFixture(t, 1, func(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
				calls.Add(1)
				return absentObservation(ctx, p)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "authority":
				w.err = errors.New("denied")
			case "cancelled":
				cancel()
			case "snapshot":
				dbtest.FailNextStatement(t, s.db, "SET TRANSACTION")
			case "deployments":
				dbtest.FailNextStatement(t, s.db, "-- name: RuntimeDeployments")
			case "targets":
				dbtest.FailNextStatement(t, s.db, "-- name: Targets :many")
			case "needs":
				dbtest.FailNextStatement(t, s.db, "-- name: NeedInstances")
			case "malformed-deployment":
				r, err := deploymentsql.New(s.db.SQL()).DeploymentByID(ctx, envs[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := deploymentsql.New(s.db.SQL()).ObserveGeneration(ctx, deploymentsql.ObserveGenerationParams{ID: r.ID, ObservedGeneration: r.ObservedGeneration, HandleJson: []byte(`[]`), Url: r.Url, Container: r.Container, HostPort: r.HostPort, ReleaseRun: r.ReleaseRun, ReleaseSha: r.ReleaseSha, ImageDigest: r.ImageDigest, Readiness: r.Readiness, UpdatedAt: r.UpdatedAt}); err != nil {
					t.Fatal(err)
				}
			case "malformed-need":
				if err := s.Needs.RecordIntent(ctx, envs[0].ID, "postgres", nil); err != nil {
					t.Fatal(err)
				}
				if _, err := deploymentsql.New(s.db.SQL()).RecordNeedIdentity(ctx, deploymentsql.RecordNeedIdentityParams{DeploymentID: envs[0].ID, Need: "postgres", HandleJson: []byte(`{"name":42}`)}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.List(ctx, projectID)
			if err == nil || got != nil || calls.Load() != 0 {
				t.Fatalf("read accepted incomplete facts: %+v %v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestListEmptyDoesNotReadTargetsOrObserve(t *testing.T) {
	t.Parallel()
	s, _, _, projectID := listingFixture(t, 0, absentObservation)
	restore := s.db.FailNextStatement("-- name: Targets :many")
	defer restore()
	got, err := s.List(context.Background(), projectID)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
	// The unused injected target fault must still be armed.
	if _, err := s.Targets.All(context.Background()); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("empty listing read targets: %v", err)
	}
}

func TestObservationSweepBoundsConcurrencyAndHonorsCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelCaller), func(t *testing.T) {
			t.Parallel()
			var active, maximum, calls atomic.Int32
			started := make(chan struct{}, 4)
			s, _, _, projectID := listingFixture(t, 9, func(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
				calls.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return deploy.Observation{}, ctx.Err()
			})
			s.observationTimeout = 200 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var err error
			go func() { err = s.ObserveRuntime(ctx); close(done) }()
			for range 4 {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("workers did not start")
				}
			}
			if cancelCaller {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("listing did not stop")
			}
			if maximum.Load() != 4 || active.Load() != 0 || calls.Load() > 9 {
				t.Fatalf("workers: max=%d active=%d calls=%d", maximum.Load(), active.Load(), calls.Load())
			}
			if cancelCaller {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled: %v", err)
				}
				return
			}
			got, readErr := s.List(context.Background(), projectID)
			if err != nil || readErr != nil || len(got) != 9 {
				t.Fatalf("deadline: %+v %v", got, err)
			}
			for _, r := range got {
				if r.Observation.State != RuntimeUnknown || !errors.Is(r.Observation.Err, context.DeadlineExceeded) {
					t.Fatalf("timeout became liveness: %+v", r)
				}
			}
		})
	}
}

func TestObservationSweepReleasesSnapshotAndRejectsChangedPlacement(t *testing.T) {
	t.Parallel()
	s, _, envs, projectID := listingFixture(t, 1, absentObservation)
	s.db.SQL().DBTX.(*sql.DB).SetMaxOpenConns(1)
	s.providers.drivers[KindDOApp] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) {
		return listingDriver{observe: func(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
			// With only one connection this cannot finish while a snapshot is held.
			_, err := deploymentsql.New(s.db.SQL()).RecordAddress(ctx, deploymentsql.RecordAddressParams{ID: envs[0].ID, Url: "https://later", UpdatedAt: time.Now().UTC()})
			return deploy.Observation{Generation: p.Generation, Rollout: "absent"}, err
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.ObserveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, projectID)
	if err != nil || len(got) != 1 || got[0].Observation.State != RuntimeUnknown || got[0].URL != "https://later" {
		t.Fatalf("snapshot held over provider I/O: %+v %v", got, err)
	}
}

func TestRuntimeObservationDoesNotInferLivenessFromRolloutFailure(t *testing.T) {
	t.Parallel()
	for _, rollout := range []string{"failed", "rolling", "unknown", ""} {
		if state := runtimeState(deploy.Observation{Rollout: rollout}); state != RuntimeUnknown || state.Running() != nil {
			t.Fatalf("%q became %v", rollout, state)
		}
	}
	stopped := RuntimeStopped.Running()
	if stopped == nil || *stopped {
		t.Fatal("confirmed stop was lost")
	}
}

func TestDisplayReadsNeverObserveAndRetainHonestFailureAge(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var unavailable atomic.Bool
	s, _, _, projectID := listingFixture(t, 1, func(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
		calls.Add(1)
		if unavailable.Load() {
			return deploy.Observation{}, errors.New("provider unavailable")
		}
		return deploy.Observation{Generation: p.Generation, Rollout: "active", Health: "healthy"}, nil
	})
	read := func() RuntimeObservation {
		t.Helper()
		v, err := s.List(context.Background(), projectID)
		if err != nil || len(v) != 1 {
			t.Fatalf("list: %+v %v", v, err)
		}
		return v[0].Observation
	}
	if v := read(); v.State != RuntimeUnknown || !v.Stale || calls.Load() != 0 {
		t.Fatalf("cold read: %+v calls=%d", v, calls.Load())
	}
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first.State != RuntimeRunning || first.Stale || first.ObservedAt.IsZero() {
		t.Fatalf("fresh: %+v", first)
	}
	unavailable.Store(true)
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := read()
	if failed.State != RuntimeRunning || !failed.Stale || failed.Err == nil || failed.ObservedAt != first.ObservedAt || failed.AttemptedAt.Before(first.AttemptedAt) {
		t.Fatalf("failed refresh rewrote history: first=%+v failed=%+v", first, failed)
	}
	for range 3 {
		read()
	}
	if calls.Load() != 2 {
		t.Fatalf("read called provider: %d", calls.Load())
	}
	records, err := s.listRecords(context.Background(), []ids.ID{projectID})
	if err != nil {
		t.Fatal(err)
	}
	if v := s.observations.read(records[0], first.ObservedAt.Add(observationStaleAfter+time.Second)); !v.Stale {
		t.Fatalf("old observation presented as fresh: %+v", v)
	}
}

func TestObservationRejectsProviderGenerationAndDeletedRows(t *testing.T) {
	t.Parallel()
	s, _, envs, projectID := listingFixture(t, 1, func(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
		return deploy.Observation{Generation: p.Generation + 1, Rollout: "active"}, nil
	})
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background(), projectID)
	if err != nil || len(rows) != 1 || rows[0].Observation.State != RuntimeUnknown || rows[0].Observation.Err == nil {
		t.Fatalf("wrong generation: %+v %v", rows, err)
	}
	// The owner removal operation reclaims the deployment; a later sweep must
	// not retain its observation even though the provider once answered.
	settleRemoval(t, s, envs[0], "alice", deploy.TakenDown{})
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.observations.entries) != 0 {
		t.Fatalf("deleted observations retained: %d", len(s.observations.entries))
	}
}

// A deployment on a provider's platform has no container: it is observed by
// the background observer's latest sample, running or stopped only when the
// fresh sample says so, and otherwise unknown with the true reason. A fresh
// sample that says neither (a rollout in progress; Portainer, which cannot
// tell) is not "out of date".
func TestAPlatformDeploymentIsObservedByItsLatestSample(t *testing.T) {
	t.Parallel()
	var rollout atomic.Pointer[string]
	s, _, envs, _ := listingFixture(t, 1, func(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
		r := *rollout.Load()
		if r == "error" {
			return deploy.Observation{}, errors.New("provider unavailable")
		}
		return deploy.Observation{Generation: p.Generation, Rollout: r}, nil
	})
	d, err := s.Deployments.ByID(context.Background(), envs[0].ID)
	if err != nil || d.Container != "" {
		t.Fatalf("a platform deployment: %+v %v", d, err)
	}
	if o := s.ObserveContainer(context.Background(), d); o.State != ContainerUnknown || o.Reason != "it has not been observed yet" {
		t.Fatalf("before any sample: %+v", o)
	}
	for _, tc := range []struct {
		rollout string
		state   ContainerPresence
		reason  string
	}{
		{"active", ContainerRunning, ""},
		{"absent", ContainerStopped, ""},
		{"rolling", ContainerUnknown, "its platform does not say whether it runs"},
		{"unknown", ContainerUnknown, "its platform does not say whether it runs"},
		{"error", ContainerUnknown, "its platform's status could not be read"},
	} {
		r := tc.rollout
		rollout.Store(&r)
		if err := s.ObserveRuntime(context.Background()); err != nil {
			t.Fatal(err)
		}
		if o := s.ObserveContainer(context.Background(), d); o.State != tc.state || o.Reason != tc.reason {
			t.Fatalf("after a %q sample: %+v", tc.rollout, o)
		}
	}
}

// fleetInventory observes a whole target's deployments in one look.
type fleetInventory struct {
	look    func()
	observe func([]deploy.Placement) ([]deploy.Observation, error)
}

func (f fleetInventory) Inventory(context.Context) ([]deploy.Resource, error) { return nil, nil }
func (f fleetInventory) ObserveAll(_ context.Context, ps []deploy.Placement) ([]deploy.Observation, error) {
	f.look()
	return f.observe(ps)
}

// A target whose inventory can observe its whole fleet is asked once per
// sweep, not once per deployment, and each deployment gets its own state.
// A failed look leaves every deployment on it unknown, never stopped: the
// last state is kept as last known, with the time it was seen.
func TestASweepAsksAFleetTargetOnceForAllItsDeployments(t *testing.T) {
	t.Parallel()
	var driverCalls atomic.Int32
	s, _, envs, projectID := listingFixture(t, 3, func(context.Context, deploy.Placement) (deploy.Observation, error) {
		driverCalls.Add(1)
		return deploy.Observation{}, errors.New("the driver was asked")
	})
	second, err := s.Targets.Put(context.Background(), &Target{Name: "cloud-2", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(context.Background(), second, "*", OfferCaps{}, "setup"); err != nil {
		t.Fatal(err)
	}
	envs = append(envs, configureForTest(t, s, ConfigureDeployment{ProjectID: projectID, Name: "app-3", Target: "cloud-2"}))
	var mu sync.Mutex
	looks := map[string]int{}
	failing := false
	s.providers.inventories = map[string]InventoryFactory{KindDOApp: func(target *Target) (deploy.Inventorier, error) {
		return fleetInventory{look: func() { mu.Lock(); looks[target.Name]++; mu.Unlock() }, observe: func(ps []deploy.Placement) ([]deploy.Observation, error) {
			if failing {
				return nil, errors.New("the provider did not answer")
			}
			out := make([]deploy.Observation, len(ps))
			for i, p := range ps {
				out[i] = deploy.Observation{Generation: p.Generation, Rollout: map[string]string{"app-0": "active", "app-1": "absent", "app-2": "unknown", "app-3": "active"}[p.Name]}
			}
			return out, nil
		}}, nil
	}}
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(context.Background(), projectID)
	if err != nil || len(got) != len(envs) {
		t.Fatalf("listing: %+v %v", got, err)
	}
	for i, want := range []RuntimeState{RuntimeRunning, RuntimeStopped, RuntimeUnknown, RuntimeRunning} {
		if got[i].Observation.State != want || got[i].Observation.Stale {
			t.Errorf("%s: %+v, want %s", got[i].Name, got[i].Observation, want)
		}
	}
	if looks["cloud"] != 1 || looks["cloud-2"] != 1 || driverCalls.Load() != 0 {
		t.Fatalf("looks per target %v, driver asked %d times", looks, driverCalls.Load())
	}
	seen := got[0].Observation.ObservedAt
	failing = true
	if err := s.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List(context.Background(), projectID)
	for _, r := range got {
		if r.Observation.Err == nil || !r.Observation.Stale {
			t.Errorf("%s after a failed look: %+v", r.Name, r.Observation)
		}
	}
	if o := got[0].Observation; o.State != RuntimeRunning || !o.ObservedAt.Equal(seen) {
		t.Errorf("the last known state was not kept with when it was seen: %+v", o)
	}
}
