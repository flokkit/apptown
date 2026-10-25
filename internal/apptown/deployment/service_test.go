package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

// The subsystem is built from its declared dependencies alone: every
// field of Dependencies is satisfied here by a value this test defines,
// and nothing from AppTown is imported (the shape checker asserts the
// import graph; this asserts the constructor).
type (
	noFollowups   struct{}
	noDeclaration struct{}
	noRetirement  struct{}
	fakeDriver    struct{ kind string }
)

func (noFollowups) EnqueueIn(context.Context, *database.Tx, jobs.Request) error {
	return nil
}
func (noDeclaration) Declared(context.Context, ids.ID) (Declared, bool) { return Declared{}, false }
func (noRetirement) RetireUnusedConnector(context.Context, *database.Tx, string, string, string, time.Time) error {
	return nil
}
func (d fakeDriver) Reconcile(context.Context, deploy.Placement, int64, deploy.Artifact, string, deploy.Logf, deploy.Checkpoint) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}
func (d fakeDriver) Resume(context.Context, deploy.Placement, int64, map[string]any, deploy.Logf) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}
func (d fakeDriver) Observe(context.Context, deploy.Placement) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}
func (d fakeDriver) Teardown(context.Context, deploy.Placement, int64, deploy.TakeDown) (deploy.TakenDown, error) {
	return deploy.TakenDown{}, nil
}
func (d fakeDriver) Controls() deploy.Controls { return deploy.Controls{} }

func TestServiceIsBuiltFromDeclaredDependencies(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	driverOverride := func(ctx context.Context, t *Target, _ *Deployment) (deploy.Driver, error) {
		return fakeDriver{t.Kind}, nil
	}
	inventoryFailure := errors.New("custom inventory")
	inventoryOverride := func(*Target) (deploy.Inventorier, error) { return nil, inventoryFailure }
	s := New(context.Background(), db, assembled(db, testRuntime{Inventories: map[string]InventoryFactory{KindDockerHost: inventoryOverride, KindPortainer: inventoryOverride, KindDOApp: inventoryOverride}, Connectors: noRetirement{}, OfferedByConstruction: []string{"lan"}, Drivers: map[string]DriverFactory{KindDockerHost: driverOverride, KindPortainer: driverOverride, KindDOApp: driverOverride}}, Dependencies{Followups: noFollowups{}, Declared: noDeclaration{}}))
	if s.Targets == nil || s.Deployments == nil || s.Releases == nil || s.Outcomes == nil ||
		s.Bundles == nil || s.Retained == nil || s.Reconciliations == nil || s.Inventory == nil {
		t.Fatal("every store is built once by New")
	}
	for _, kind := range []string{KindDockerHost, KindPortainer, KindDOApp} {
		target := &Target{Kind: kind}
		d, err := s.providers.Driver(context.Background(), target, &Deployment{})
		if err != nil || d.(fakeDriver).kind != kind {
			t.Fatalf("registered %s driver: %v %v", kind, d, err)
		}
		if _, err := s.providers.TargetInventory(target); !errors.Is(err, inventoryFailure) {
			t.Fatalf("registered %s inventory: %v", kind, err)
		}
	}
	if _, err := s.providers.Driver(context.Background(), &Target{Kind: "unknown"}, &Deployment{}); err == nil {
		t.Fatal("an unregistered kind is refused, not guessed")
	}
	if _, err := s.Targets.All(context.Background()); err != nil {
		t.Fatalf("the stores work against the database: %v", err)
	}
}
