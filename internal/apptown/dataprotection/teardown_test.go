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
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
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
		return d.taken, d.remove(ctx, p, g)
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
	if err := deployment.NewNeedStore(s.db, nil).RecordIntent(context.Background(), dep.ID, "postgres", map[string]any{"name": "retained-pg"}); err != nil {
		t.Fatal(err)
	}
	if err := deployment.NewNeedStore(s.db, nil).RecordIdentity(context.Background(), dep.ID, "postgres", nil, "db-1", "retained-pg"); err != nil {
		t.Fatal(err)
	}
	return s, w, driver, dep
}
func removeSelection(dep *deployment.Deployment) RemoveDeployment {
	return RemoveDeployment{ProjectID: dep.ProjectID, Name: dep.Name, WithoutFinalCopy: dep.Name}
}

func TestRemoveRetainsManagedNeedsAndRecordsTogether(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	// Emulator rows are not produced by managed-need operations. Seed one here
	// to prove the persistence distinction, without starting a real emulator.
	// sql-guarantee: TestRemoveRetainsManagedNeedsAndRecordsTogether proves emulator rows are excluded from retention; RecordNeedIntent and RecordNeedFulfilled hardcode managed mode and cannot create this projection.
	mustExec(t, s.db, `INSERT INTO need_instances(id,deployment_id,need,mode,status,updated_at) VALUES ($1,$2,'redis','emulator','ready','2026-10-01')`, ids.New(time.Now()), dep.ID)
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
	for _, scenario := range []string{"denied", "missing caller", "missing deployment", "follows main", "attempt", "release", "cancelled"} {
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
			case "follows main":
				in.Name = "automatic"
				if _, err := s.Deployments.EnsureStanding(t.Context(), dep.ProjectID, in.Name, dep.TargetID, deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}); err != nil {
					t.Fatal(err)
				}
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
	for _, scenario := range []string{"provider", "retention", "delete", "audit"} {
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
				when := ""
				if scenario == "audit" {
					when = " WHEN (NEW.kind = 'deployment.torn-down')"
				}
				mustExec(t, s.db, `CREATE TRIGGER veto_removal BEFORE `+event+` ON `+table+` FOR EACH ROW`+when+` EXECUTE FUNCTION veto_removal()`)
			}
			result, err := s.Remove(ctx, removeSelection(dep))
			if err == nil || result.Retained != nil {
				t.Fatalf("incomplete outcome: %+v %v", result, err)
			}
			if scenario != "provider" && scenario != "cancelled" && !strings.Contains(err.Error(), "records were not removed") {
				t.Fatalf("settlement detail: %v", err)
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
			if err == nil || !(strings.Contains(err.Error(), "deployment changed during removal") || strings.Contains(err.Error(), "deployment.take-down is running")) {
				t.Fatalf("stale completion: %v", err)
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

// A promotion reference refuses removal before a provider call; all
// dependants are named so their feeds can be changed.
func TestPromotionSourceRemovalRefusesBeforeEffects(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	for _, name := range []string{"production", "production-west"} {
		downstream, err := s.Deployments.EnsureStanding(t.Context(), dep.ProjectID, name, dep.TargetID, deployment.Attributes{}.WithDefaults())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetFeed(t.Context(), dep.ProjectID, name, deployment.FeedSettings{Revision: downstream.FeedRevision, Feed: "promotion", From: dep.Name}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := s.Remove(t.Context(), removeSelection(dep))
	if err == nil || !strings.Contains(err.Error(), "production, production-west") || !strings.Contains(err.Error(), "change their feeds first") {
		t.Errorf("source removal refusal: %v", err)
	}
	if driver.calls.Load() != 0 {
		t.Errorf("refused source removal called provider %d times", driver.calls.Load())
	}
	if !deploymentExists(t, s.db, dep.ID) {
		t.Fatal("source deployment disappeared")
	}
	wantN(t, "need instances", needInstances(t, s.db, dep.ID), 1)
	wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down", bySubject(dep.Name)), 0)
}

// A lost answer is uncertainty, not proof the resource is still present.
// Retrying while the deployment stands uses the same removal operation.
func TestRemovalRecordsPartialEffectsAndResumes(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost answer", true: "refused"}[refused], func(t *testing.T) {
			s, _, driver, dep := removalFixture(t)
			ops, _ := takeDownOps(t, s)
			owned := []deploy.Owned{{Kind: "app", ID: "deleted"}, {Kind: "network", ID: "left"}, {Kind: "route", ID: "uncertain"}}
			for _, o := range owned {
				if err := s.Deployments.OwnerOf(dep).Created(t.Context(), o); err != nil {
					t.Fatal(err)
				}
			}
			driver.taken = deploy.TakenDown{Deleted: []string{owned[0].Label()}, Left: []deploy.Left{{What: "network", Handle: map[string]any{"id": "left"}, Why: "in use"}}}
			driver.remove = func(context.Context, deploy.Placement, int64) error {
				if refused {
					return deploy.Terminal(errors.New("provider refused deletion"))
				}
				return errors.New("lost delete response")
			}
			op, err := s.TakeDown(t.Context(), removeSelection(dep))
			if err != nil {
				t.Fatal(err)
			}
			done, err := ops.RunHere(t.Context(), op.ID)
			want := operations.Unknown
			if refused {
				want = operations.Failed
			}
			if err != nil || done.State != want {
				t.Fatalf("outcome: %+v %v", done, err)
			}
			for _, words := range []string{"deleted (confirmed): app deleted", "left: network", "in use", "not confirmed removed: route uncertain", "Take it down again"} {
				if !strings.Contains(done.Detail, words) {
					t.Errorf("missing %q in %s", words, done.Detail)
				}
			}
			if !deploymentExists(t, s.db, dep.ID) {
				t.Fatal("partial failure lost record")
			}
			driver.remove = nil
			op, err = s.TakeDown(t.Context(), removeSelection(dep))
			if err != nil {
				t.Fatal(err)
			}
			done, err = ops.RunHere(t.Context(), op.ID)
			if err != nil || done.State != operations.Succeeded || deploymentExists(t, s.db, dep.ID) || !strings.Contains(done.Detail, "in use") {
				t.Fatalf("retry: %+v %v", done, err)
			}
		})
	}
}

func TestRemovalSettlementFailureKeepsItsFinalSet(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "settlement-final")
	setCopies(client, func(req docker.Command) ([]byte, error) {
		_, err := containerresulttest.WriteString(req.Stdout, "copy")
		return nil, err
	})
	ops, _ := takeDownOps(t, s)
	s.applied = func(ids.ID) { dbtest.FailNextStatement(t, s.db, "-- name: DeleteDeployment") }
	op, err := s.TakeDown(t.Context(), removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	done, err := ops.RunHere(t.Context(), op.ID)
	if err != nil || done.State != operations.Failed || !strings.Contains(done.Detail, "teardown finished") || !strings.Contains(done.Detail, "records were not removed") {
		t.Fatalf("settlement: %+v %v", done, err)
	}
	id := inputID(done.Result, "finalBackup")
	if id.IsZero() || !strings.Contains(done.Detail, id.String()) {
		t.Fatalf("lost final-set evidence: %+v", done)
	}
	if _, err := s.backups.ByID(t.Context(), dep.ProjectID, dep.Name, id); err != nil {
		t.Fatalf("final set lost: %v", err)
	}
	if !deploymentExists(t, s.db, dep.ID) {
		t.Fatal("failed settlement lost record")
	}
}

func TestAClosedRequestCannotStopItsTakeDown(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	ops, _ := takeDownOps(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	op, err := s.TakeDown(ctx, removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done, err := ops.RunHere(t.Context(), op.ID)
	if err != nil || done.State != operations.Succeeded || driver.calls.Load() != 1 {
		t.Fatalf("closed request stopped operation: %+v %v", done, err)
	}
}
