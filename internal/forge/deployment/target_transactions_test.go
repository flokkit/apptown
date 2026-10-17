package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

type integrationRetirement struct{}

func (integrationRetirement) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, target, who string, at time.Time) error {
	return integration.RetireConnectorIn(ctx, tx, provider, digitalocean.CapCloudAccount, target, who, at)
}

func targetRetirementFixture(t *testing.T) (*database.DB, *Targets, *Target) {
	t.Helper()
	db := testDB(t)
	if _, err := integration.New(db).Save(context.Background(), "retiring-account", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	targets := NewTargets(db, integrationRetirement{})
	target, err := targets.Put(context.Background(), &Target{Name: "retiring-target", Kind: KindDOApp, Provider: "retiring-account", Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"*", "team"} {
		if err := targets.Offer(context.Background(), target, group, OfferCaps{}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	return db, targets, target
}

func TestTargetRetirementCompetesWithBindingsAndOffers(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	// A project is an external prerequisite; the test owns Deployment's
	// target, offer, binding, and retirement transactions.
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "retire-race", Name: "Retire race"})
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := NewTargets(db, noRetirement{})
	deployments := NewDeployments(db)
	for _, operation := range []string{"bind", "offer"} {
		for i := 0; i < 6; i++ {
			target, err := targets.Put(context.Background(), &Target{Name: fmt.Sprintf("%s-%d", operation, i), Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			results := race(2, func(i int) int {
				var err error
				if i == 0 {
					err = targets.Retire(context.Background(), target.Name, "alice")
				} else if operation == "bind" {
					_, err = deployments.EnsureStanding(t.Context(), projectID, target.Name, target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
				} else {
					err = targets.Offer(context.Background(), target, "*", OfferCaps{}, "alice")
				}
				if err != nil {
					if !errors.Is(err, ErrTargetConflict) {
						t.Error(err)
					}
					return 0
				}
				return 1
			})
			if operation == "bind" && count(results, 1) != 1 {
				t.Fatalf("binding and retirement must have one winner: %v", results)
			}
			if operation == "offer" && results[0] != 1 {
				t.Fatalf("an offer prevented retirement: %v", results)
			}
		}
	}
	all, err := targets.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range all {
		if target.Status != TargetRetired {
			continue
		}
		if bound := deploymentsWhere(t, db, func(d *Deployment) bool { return d.TargetID == target.ID }); bound != 0 {
			t.Fatalf("retired target %s has %d deployments", target.Name, bound)
		}
		wantN(t, "offers on retired "+target.Name, targetOffers(t, targets, target.ID), 0)
	}
}

func targetStatus(t *testing.T, targets *Targets, id ids.ID) TargetState {
	t.Helper()
	target, err := targets.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return target.Status
}

func targetOffers(t *testing.T, targets *Targets, id ids.ID) []Offer {
	t.Helper()
	offers, err := targets.Offers(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return offers
}

// connectors is how many connectors of any capability carry the name, as the
// integration owner reads it (names are unique, so 0 or 1).
func connectors(t *testing.T, db *database.DB, name string) int {
	t.Helper()
	_, err := integration.New(db).ByName(t.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return 1
}

func retirementReceipts(t *testing.T, db *database.DB, filters ...func(audit.Entry) bool) int {
	t.Helper()
	return len(receipts(t, db, "target.retired", filters...)) + len(receipts(t, db, "integration.deleted", filters...))
}

func TestTargetRetirementRollsBackMissingRecords(t *testing.T) {
	t.Parallel()
	for _, record := range []string{"targets", "target_offers", "integrations", "target.retired", "integration.deleted"} {
		t.Run(record, func(t *testing.T) {
			db, targets, target := targetRetirementFixture(t)
			table, operation, body := record, "DELETE", "RETURN NULL;"
			switch record {
			case "targets":
				operation = "UPDATE"
			case "target_offers":
				body = "IF OLD.group_name='*' THEN RETURN NULL; END IF; RETURN OLD;"
			case "target.retired", "integration.deleted":
				table, operation = "audit_log", "INSERT"
				body = "IF NEW.kind='" + record + "' THEN RETURN NULL; END IF; RETURN NEW;"
			}
			mustExec(t, db, `CREATE FUNCTION veto_target_retirement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
			mustExec(t, db, `CREATE TRIGGER veto_target_retirement BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_target_retirement()`)
			if err := targets.Retire(context.Background(), target.Name, "alice"); err == nil || errors.Is(err, ErrTargetMissing) {
				t.Fatalf("missing write was accepted or mistaken for a missing target: %v", err)
			}
			if s := targetStatus(t, targets, target.ID); s != TargetReady {
				t.Fatalf("target after a refused retirement: %s", s)
			}
			wantN(t, "offers", targetOffers(t, targets, target.ID), 2)
			wantCount(t, "connectors", connectors(t, db, target.Provider), 1)
			wantCount(t, "retirement receipts", retirementReceipts(t, db), 0)
			mustExec(t, db, `DROP TRIGGER veto_target_retirement ON `+table)
			if err := targets.Retire(context.Background(), target.Name, "alice"); err != nil {
				t.Fatal(err)
			}
			if s := targetStatus(t, targets, target.ID); s != TargetRetired {
				t.Fatalf("target after retirement: %s", s)
			}
			wantN(t, "offers", targetOffers(t, targets, target.ID), 0)
			wantCount(t, "connectors", connectors(t, db, target.Provider), 0)
			wantCount(t, "retirement receipts by alice", retirementReceipts(t, db, byActor("alice")), 2)
		})
	}
}

func TestTargetRetirementSharedConnectorAndReplay(t *testing.T) {
	t.Parallel()
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint("concurrent=", concurrent), func(t *testing.T) {
			db, targets, target := targetRetirementFixture(t)
			other := *target
			other.Name = "sibling-target"
			if _, err := targets.Put(context.Background(), &other); err != nil {
				t.Fatal(err)
			}
			if concurrent {
				race(2, func(i int) int {
					if err := targets.Retire(context.Background(), []string{target.Name, other.Name}[i], "alice"); err != nil {
						t.Error(err)
					}
					return 0
				})
			} else {
				if err := targets.Retire(context.Background(), target.Name, "alice"); err != nil {
					t.Fatal(err)
				}
				wantCount(t, "connectors", connectors(t, db, target.Provider), 1)
				if err := targets.Retire(context.Background(), other.Name, "alice"); err != nil {
					t.Fatal(err)
				}
			}
			wantCount(t, "connectors", connectors(t, db, target.Provider), 0)
			race(8, func(int) int {
				if err := targets.Retire(context.Background(), target.Name, "alice"); err != nil {
					t.Error(err)
				}
				return 0
			})
			wantN(t, "target.retired receipts", receipts(t, db, "target.retired"), 2)
			wantN(t, "integration.deleted receipts", receipts(t, db, "integration.deleted"), 1)
			got, err := targets.Put(context.Background(), target)
			if err != nil || got.Status != TargetRetired {
				t.Fatalf("stale refresh revived target: %+v %v", got, err)
			}
		})
	}
}

type retirementBarrier struct {
	inner            ConnectorRetirement
	entered, release chan struct{}
}

func (b retirementBarrier) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, target, who string, at time.Time) error {
	close(b.entered)
	<-b.release
	return b.inner.RetireUnusedConnector(ctx, tx, provider, target, who, at)
}

func TestTargetRetirementHoldsNonUseThroughConnectorRemoval(t *testing.T) {
	t.Parallel()
	db, _, target := targetRetirementFixture(t)
	barrier := retirementBarrier{inner: integrationRetirement{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	defer release()
	targets := NewTargets(db, barrier)
	retired := make(chan error, 1)
	go func() { retired <- targets.Retire(context.Background(), target.Name, "alice") }()
	select {
	case <-barrier.entered:
	case err := <-retired:
		t.Fatalf("retirement failed before handoff: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not reach connector")
	}
	created := make(chan error, 1)
	go func() {
		_, err := targets.Put(context.Background(), &Target{Name: "new-sibling", Kind: KindDOApp, Provider: target.Provider, Origin: OriginAttached, Status: TargetReady})
		created <- err
	}()
	dbtest.WaitForLock(t, db, "-- name: PutTarget")
	wantCount(t, "connectors", connectors(t, db, target.Provider), 1)
	release()
	for _, done := range []chan error{retired, created} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("target operation did not finish")
		}
	}
	wantCount(t, "connectors", connectors(t, db, target.Provider), 0)
}

func TestTargetCheckAndOfferRequireReceipts(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"check", "offer"} {
		t.Run(operation, func(t *testing.T) {
			db := testDB(t)
			targets := NewTargets(db, noRetirement{})
			target, err := targets.Put(context.Background(), &Target{Name: "receipt-target", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range []string{"*", "team"} {
				if err := targets.Offer(context.Background(), target, group, OfferCaps{}, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			mustExec(t, db, `CREATE FUNCTION veto_target_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			mustExec(t, db, `CREATE TRIGGER veto_target_receipt BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION veto_target_receipt()`)
			if operation == "check" {
				_, err = targets.RecordCheck(context.Background(), target, false, "alice", map[string]any{"ok": false})
			} else {
				err = targets.Offer(context.Background(), target, "new-group", OfferCaps{}, "alice")
			}
			if err == nil {
				t.Fatal("accepted a missing receipt")
			}
			if s := targetStatus(t, targets, target.ID); s != TargetReady {
				t.Fatalf("target after a missing receipt: %s", s)
			}
			wantN(t, "offers", targetOffers(t, targets, target.ID), 2)
		})
	}
}
