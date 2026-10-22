package dataprotection

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
)

// The fake provider is injected at the service boundary; ordinary bindings and
// managed needs are created through domain operations.
type removalDriver struct {
	fakeDriver
	calls  atomic.Int32
	remove func(context.Context, deploy.Placement, int64) error
	got    deploy.TakeDown
	taken  deploy.TakenDown
}

func (d *removalDriver) Teardown(ctx context.Context, p deploy.Placement, g int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	d.calls.Add(1)
	d.got = td
	if d.remove != nil {
		return deploy.TakenDown{}, d.remove(ctx, p, g)
	}
	return d.taken, nil
}
func removalFixture(t *testing.T) (*both, *configurationWorld, *removalDriver, *deployment.Deployment) {
	t.Helper()
	driver := &removalDriver{}
	s, w, in, _ := configurationFixtureWith(t, testRuntime{Drivers: map[string]deployment.DriverFactory{deployment.KindDOApp: func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
		return driver, nil
	}}})
	dep := configureForTest(t, s, in)
	if err := s.Needs.RecordIntent(context.Background(), dep.ID, "postgres", map[string]any{"name": "retained-pg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Needs.RecordIdentity(context.Background(), dep.ID, "postgres", nil, "db-1", "retained-pg"); err != nil {
		t.Fatal(err)
	}
	return s, w, driver, dep
}
func removeSelection(dep *deployment.Deployment) RemoveDeployment {
	return RemoveDeployment{ProjectID: dep.ProjectID, Name: dep.Name}
}

func TestRemoveRetainsManagedNeedsAndRecordsTogether(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	// Emulator rows are not produced by managed-need operations. Seed one here
	// to prove the persistence distinction, without starting a real emulator.
	// sql-guarantee: TestRemoveRetainsManagedNeedsAndRecordsTogether proves emulator rows are excluded from retention; RecordNeedIntent and RecordNeedFulfilled hardcode managed mode and cannot create this projection.
	mustExec(t, s.db, `INSERT INTO need_instances(id,deployment_id,need,mode,status,updated_at) VALUES (?,?,'redis','emulator','ready','2026-10-01')`, ids.New(time.Now()), dep.ID)
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil || len(result.Retained) != 1 || !strings.HasPrefix(result.Retained[0], "postgres: retained-pg") {
		t.Fatalf("removal: %+v %v", result, err)
	}
	if driver.calls.Load() != 1 {
		t.Fatal("provider call missing")
	}
	if deploymentExists(t, s.db, dep.ID) {
		t.Fatal("removed deployment still stands")
	}
	wantN(t, "need instances", needInstances(t, s.db, dep.ID), 0)
	resources, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil || len(resources) != 1 {
		t.Fatalf("retained: %+v %v", resources, err)
	}
	if resources[0].Handle["id"] != "db-1" || resources[0].RetainedBy != "alice" || resources[0].Disposition != deployment.RetainedOpen {
		t.Fatalf("retained identity: %+v", resources[0])
	}
	wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down", byActor("alice"), bySubject(dep.Name)), 1)
}
func TestRemoveRefusesBeforeProviderWork(t *testing.T) {
	for _, scenario := range []string{"denied", "missing caller", "missing deployment", "main", "attempt", "release", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, w, driver, dep := removalFixture(t)
			in := removeSelection(dep)
			ctx := context.Background()
			switch scenario {
			case "denied":
				w.denial = errors.New("denied")
			case "missing caller":
				w.actor.Name = ""
			case "missing deployment":
				in.Name = "missing"
			case "main":
				in.Name = "main"
				configureForTest(t, s, deployment.ConfigureDeployment{ProjectID: dep.ProjectID, Name: "main", Target: "cloud"})
			case "attempt":
				if _, _, err := s.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "running"}); err != nil {
					t.Fatal(err)
				}
			case "release":
				if _, err := s.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: dep.Name, Actor: "alice"}, "started"); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := s.Remove(ctx, in)
			if err == nil || result.Retained != nil {
				t.Fatalf("refused removal: %+v %v", result, err)
			}
			if driver.calls.Load() != 0 {
				t.Fatal("refused request called provider")
			}
			if !deploymentExists(t, s.db, dep.ID) {
				t.Fatal("refused removal deleted the deployment")
			}
			wantN(t, "retained resources", everyRetained(t, s.db), 0)
		})
	}
}
func TestRemoveIncompleteProviderOrReceiptPreservesRecords(t *testing.T) {
	for _, scenario := range []string{"provider", "cancelled", "retention", "delete", "audit"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, _, driver, dep := removalFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "provider":
				driver.remove = func(context.Context, deploy.Placement, int64) error { return errors.New("lost provider response") }
			case "cancelled":
				driver.remove = func(context.Context, deploy.Placement, int64) error { cancel(); return nil }
			default:
				table := map[string]string{"retention": "retained_resources", "delete": "deployments", "audit": "audit_log"}[scenario]
				event := "INSERT"
				if scenario == "delete" {
					event = "DELETE"
				}
				mustExec(t, s.db, `CREATE FUNCTION veto_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				mustExec(t, s.db, `CREATE TRIGGER veto_removal BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_removal()`)
			}
			result, err := s.Remove(ctx, removeSelection(dep))
			var incomplete *deployment.RemovalIncompleteError
			if !errors.As(err, &incomplete) || result.Retained != nil {
				t.Fatalf("incomplete outcome: %+v %v", result, err)
			}
			if incomplete.ProviderAccepted != (scenario != "provider") {
				t.Fatalf("provider result: %+v", incomplete)
			}
			if !deploymentExists(t, s.db, dep.ID) {
				t.Fatal("incomplete removal deleted the deployment")
			}
			wantN(t, "need instances", needInstances(t, s.db, dep.ID), 1)
			wantN(t, "retained resources", everyRetained(t, s.db), 0)
			wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down"), 0)
		})
	}
}

// A deployment replaced under its name while the provider is asked is
// refused by Deployment's settlement (deployment's
// TestRemovalSettlementRefusesAReplacedPlacement).
func TestRemoveRejectsChangedSelectionAfterProviderCall(t *testing.T) {
	for _, change := range []string{"binding", "generation"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, driver, dep := removalFixture(t)
			driver.remove = func(_ context.Context, p deploy.Placement, g int64) error {
				if p.ID != dep.ID || g != dep.ObservedGeneration {
					t.Fatal("driver received wrong selection")
				}
				// These writes can complete only if Remove holds no transaction across I/O.
				switch change {
				case "binding":
					target, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "other", Kind: deployment.KindDOApp, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
					if err != nil {
						return err
					}
					_, err = s.Configure(context.Background(), deployment.ConfigureDeployment{ProjectID: dep.ProjectID, Name: dep.Name, Target: target.Name})
					return err
				case "generation":
					_, _, err := s.Outcomes.Admit(context.Background(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "new-generation"})
					return err
				}
				return nil
			}
			_, err := s.Remove(context.Background(), removeSelection(dep))
			var incomplete *deployment.RemovalIncompleteError
			if !errors.As(err, &incomplete) || !incomplete.ProviderAccepted {
				t.Fatalf("stale completion: %v", err)
			}
			if !errors.Is(err, deployment.ErrRemovalChanged) && !errors.Is(err, deployment.ErrRemovalBusy) {
				t.Fatalf("wrong stale refusal: %v", err)
			}
			if _, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); err != nil {
				t.Fatalf("changed selection lost its deployment: %v", err)
			}
			wantN(t, "retained resources", everyRetained(t, s.db), 0)
			wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down"), 0)
		})
	}
}
func TestRemoveSettlementFailureCanBeExplicitlyRetried(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	dbtest.FailNextStatement(t, s.db, "-- name: DeleteDeployment")
	if _, err := s.Remove(context.Background(), removeSelection(dep)); err == nil {
		t.Fatal("hidden settlement failure")
	}
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil || len(result.Retained) != 1 {
		t.Fatalf("explicit retry: %+v %v", result, err)
	}
	if driver.calls.Load() != 2 {
		t.Fatalf("provider calls: %d", driver.calls.Load())
	}
	wantN(t, "retained resources", everyRetained(t, s.db), 1)
	wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down"), 1)
}

// A take-down's records carry AppTown's clock, however far it is from
// the machine's: what it retained, and its receipt.
func TestATakeDownIsStampedByAppTownsClock(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	at := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	s.setClock(func() time.Time { return at })
	if _, err := s.Remove(context.Background(), removeSelection(dep)); err != nil {
		t.Fatal(err)
	}
	resources, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil || len(resources) != 1 || !resources[0].RetainedAt.Time.Equal(at) {
		t.Fatalf("retained %+v %v, want it at AppTown's %v", resources, err, at)
	}
	lines, err := audit.Recent(context.Background(), s.db, audit.Filter{KindPrefix: "deployment.torn-down", Limit: 1})
	if err != nil || len(lines) != 1 || lines[0].At != at.Format(time.RFC3339) {
		t.Fatalf("receipt %+v %v, want it stamped %v", lines, err, at)
	}
}
