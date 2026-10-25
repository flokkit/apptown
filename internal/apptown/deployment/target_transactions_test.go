package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type integrationRetirement struct{}

func (integrationRetirement) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, target, who string, at time.Time) error {
	return integration.RetireConnectorIn(ctx, tx, provider, digitalocean.CapCloudAccount, target, who, at)
}

func targetRetirementFixture(t *testing.T) (*database.DB, *Targets, *Target) {
	t.Helper()
	db := testDB(t)
	if _, err := integration.NewWithClock(db, nil).Save(context.Background(), "retiring-account", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
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
	_, err := integration.NewWithClock(db, nil).ByName(t.Context(), name)
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

// The target name is a durable identity: probing before Attach is not a lock.
func TestConcurrentTargetAttachmentHasOneWinner(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	targets := NewTargets(db, noRetirement{})
	saved := make([]*Target, 2)
	codes := race(2, func(i int) int {
		var err error
		saved[i], err = targets.Attach(t.Context(), &Target{Name: "same-name", Kind: KindDockerHost, Origin: OriginAttached,
			Address: map[string]any{"host": fmt.Sprintf("host-%d", i)}, Status: TargetReady}, Attachment{By: "fixture", Attached: map[string]any{"candidate": i}, Answer: map[string]any{"candidate": i}})
		if err == nil {
			return 1
		}
		if fault.KindOf(err) == fault.Conflict {
			return 2
		}
		t.Errorf("unexpected attachment failure: %v", err)
		return 0
	})
	if count(codes, 1) != 1 || count(codes, 2) != 1 {
		t.Fatalf("want one attachment and one conflict: %v", codes)
	}
	winner := 0
	if codes[1] == 1 {
		winner = 1
	}
	actual, err := targets.ByName(t.Context(), "same-name")
	if err != nil || !reflect.DeepEqual(actual, saved[winner]) {
		t.Fatalf("winner changed: got=%+v want=%+v err=%v", actual, saved[winner], err)
	}
	if actual.DisplayLabel != actual.Name {
		t.Fatalf("new attachment label %q, want %q", actual.DisplayLabel, actual.Name)
	}
	receipts, err := audit.Recent(t.Context(), db, audit.Filter{Subject: "same-name", KindPrefix: "target.", Limit: 10})
	if err != nil || len(receipts) != 2 {
		t.Fatalf("attachment receipts: %+v %v", receipts, err)
	}
	kinds := map[string]bool{}
	for _, receipt := range receipts {
		kinds[receipt.Kind] = true
		if string(receipt.Payload) != fmt.Sprintf(`{"candidate":%d}`, winner) {
			t.Errorf("losing attachment receipt: %+v", receipt)
		}
	}
	if !kinds["target.attached"] || !kinds["target.checked"] {
		t.Fatalf("wrong receipt pair: %+v", receipts)
	}
}

func TestDuplicateTargetAttachmentPreservesActiveAndRetiredRecords(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		t.Run(fmt.Sprint(retired), func(t *testing.T) {
			db := testDB(t)
			targets := NewTargets(db, noRetirement{})
			initial, err := targets.Attach(t.Context(), &Target{Name: "kept", Kind: KindDockerHost, Provider: "original", Origin: OriginAttached,
				Address: map[string]any{"host": "original"}, Ingress: map[string]any{"domain": "original.example"}, NeedsMode: "emulators", SecretsDelivery: "inject", Status: TargetReady}, Attachment{By: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			revision, yes := initial.SettingsRevision, true
			if _, err = targets.EditSettings(t.Context(), initial.Name, TargetSettingsEdit{ExpectedRevision: &revision, Previews: &yes}, "fixture"); err != nil {
				t.Fatal(err)
			}
			if retired {
				if err = targets.Retire(t.Context(), initial.Name, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := targets.ByName(t.Context(), initial.Name)
			if err != nil {
				t.Fatal(err)
			}
			replacement := &Target{Name: initial.Name, Kind: KindS3Bucket, Provider: "replacement", Origin: OriginProvisioned,
				Address: map[string]any{"endpoint": "other"}, Ingress: map[string]any{"domain": "other.example"}, NeedsMode: "managed", SecretsDelivery: "other", Status: TargetReady}
			if _, err = targets.Attach(t.Context(), replacement, Attachment{By: "fixture"}); fault.KindOf(err) != fault.Conflict {
				t.Fatalf("duplicate attachment: %v", err)
			}
			after, err := targets.ByName(t.Context(), initial.Name)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("existing target changed: before=%+v after=%+v err=%v", before, after, err)
			}
			for _, kind := range []string{"target.attached", "target.checked"} {
				receipts, err := audit.Recent(t.Context(), db, audit.Filter{Subject: initial.Name, KindPrefix: kind, Limit: 10})
				if err != nil || len(receipts) != 1 {
					t.Fatalf("duplicate receipt %s: %+v %v", kind, receipts, err)
				}
			}
		})
	}
}

func TestCloudAttachmentCommitsOnlyWithANewBindingAndTarget(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "binding", "managed-binding", "target", "retired-target", "binding-insert", "binding-version", "first-receipt", "second-receipt", "third-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			integrations := integration.NewService(db, integration.Dependencies{})
			targets := NewTargets(db, noRetirement{})
			service := &Service{db: db, Targets: targets, providers: &Providers{integrations: integrations}}
			var beforeBinding *integration.Binding
			var beforeTarget *Target
			if scenario == "binding" || scenario == "managed-binding" {
				if _, err := integrations.Bindings.Upsert(t.Context(), "account", digitalocean.CapCloudAccount, "digitalocean", `{"tokenFrom":"OLD","region":"old"}`, scenario == "managed-binding"); err != nil {
					t.Fatal(err)
				}
				var err error
				beforeBinding, err = integrations.Bindings.ByName(t.Context(), "account")
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "target" || scenario == "retired-target" {
				var err error
				beforeTarget, err = targets.Put(t.Context(), &Target{Name: "account", Kind: KindDockerHost, Origin: OriginAttached, Address: map[string]any{"host": "original"}, Status: TargetReady})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "retired-target" {
					if err = targets.Retire(t.Context(), "account", "fixture"); err != nil {
						t.Fatal(err)
					}
					beforeTarget, err = targets.ByName(t.Context(), "account")
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			switch scenario {
			case "binding-insert":
				t.Cleanup(db.FailNextStatement("-- name: CreateBinding"))
			case "binding-version":
				t.Cleanup(db.FailNextStatement("-- name: InsertBindingVersion"))
			case "first-receipt":
				t.Cleanup(db.FailStatementAfter("-- name: AppendReceipt", 0))
			case "second-receipt":
				t.Cleanup(db.FailStatementAfter("-- name: AppendReceipt", 1))
			case "third-receipt":
				t.Cleanup(db.FailStatementAfter("-- name: AppendReceipt", 2))
			}
			saved, err := service.AttachCloudTarget(t.Context(), "account", map[string]any{"tokenFrom": "NEW", "region": "nyc"}, Attachment{By: "fixture"})
			if scenario == "success" {
				if err != nil || saved == nil || saved.Address["region"] != "nyc" || saved.Address["instanceSize"] != "basic-xxs" || saved.Address["dbSize"] != "db-s-1vcpu-1gb" {
					t.Fatalf("cloud target: %+v %v", saved, err)
				}
			} else {
				if err == nil || saved != nil {
					t.Fatalf("failed cloud admission returned %+v %v", saved, err)
				}
				if (beforeBinding != nil || beforeTarget != nil) && fault.KindOf(err) != fault.Conflict {
					t.Fatalf("duplicate: %v", err)
				}
			}
			binding, be := integrations.Bindings.ByName(t.Context(), "account")
			target, te := targets.ByName(t.Context(), "account")
			if scenario == "success" {
				if be != nil || binding.Version != 1 || binding.Managed || te != nil || target.Provider != binding.Name {
					t.Fatalf("split cloud admission: binding=%+v/%v target=%+v/%v", binding, be, target, te)
				}
			} else {
				if beforeBinding == nil {
					if !errors.Is(be, sql.ErrNoRows) {
						t.Fatalf("binding survived refusal: %+v %v", binding, be)
					}
				} else if be != nil || !reflect.DeepEqual(binding, beforeBinding) {
					t.Fatalf("binding changed: %+v %v", binding, be)
				}
				if beforeTarget == nil {
					if !errors.Is(te, sql.ErrNoRows) {
						t.Fatalf("target survived refusal: %+v %v", target, te)
					}
				} else if te != nil || !reflect.DeepEqual(target, beforeTarget) {
					t.Fatalf("target changed: %+v %v", target, te)
				}
			}
			for _, kind := range []string{"target.attached", "target.checked", "integration.saved"} {
				receipts, e := audit.Recent(t.Context(), db, audit.Filter{Subject: "account", KindPrefix: kind, Limit: 10})
				want := 0
				if scenario == "success" {
					want = 1
				}
				if e != nil || len(receipts) != want {
					t.Fatalf("cloud receipts %s: %+v %v", kind, receipts, e)
				}
			}
		})
	}
}

func TestCloudAttachmentValidatesConfigurationBeforeDefaults(t *testing.T) {
	t.Parallel()
	for _, config := range []map[string]any{nil, {"region": "nyc"}, {"tokenFrom": "TOKEN"}, {"tokenFrom": " ", "region": "nyc"}, {"tokenFrom": "TOKEN", "region": 2}, {"tokenFrom": "TOKEN", "region": "nyc", "registry": true}} {
		db := testDB(t)
		integrations := integration.NewService(db, integration.Dependencies{})
		service := &Service{db: db, Targets: NewTargets(db, noRetirement{}), providers: &Providers{integrations: integrations}}
		if _, err := service.AttachCloudTarget(t.Context(), "invalid", config, Attachment{By: "fixture"}); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("invalid config %v: %v", config, err)
		}
		if _, err := integrations.Bindings.ByName(t.Context(), "invalid"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("invalid config wrote binding: %v", err)
		}
		if _, err := service.Targets.ByName(t.Context(), "invalid"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("invalid config wrote target: %v", err)
		}
	}
}

func TestInvalidOfferCapsPreserveTheOfferAndItsReceipts(t *testing.T) {
	db, targets, target := targetRetirementFixture(t)
	ctx := context.Background()
	if err := targets.Offer(ctx, target, "*", OfferCaps{MaxStanding: 3}, "fixture"); err != nil {
		t.Fatal(err)
	}
	before, err := targets.Offers(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := len(receipts(t, db, "target.offered"))
	for _, caps := range []OfferCaps{{MaxStanding: -1}, {Audiences: []string{"unknown"}}, {Data: []string{"unknown"}}} {
		if err := targets.Offer(ctx, target, "*", caps, "fixture"); fault.KindOf(err) != fault.Unprocessable {
			t.Fatalf("invalid caps did not return unprocessable: %v", err)
		}
		after, err := targets.Offers(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("refused caps changed offers: before=%v after=%v", before, after)
		}
		if got := len(receipts(t, db, "target.offered")); got != count {
			t.Fatalf("refused caps wrote receipt: %d -> %d", count, got)
		}
	}
}
