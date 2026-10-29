package deployment

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func addressFixture(t *testing.T) (*Service, *configurationWorld, *Deployment, *Target) {
	t.Helper()
	s, authority, in, _ := configurationFixture(t)
	target, err := s.Targets.Put(t.Context(), &Target{Name: "address-host", Kind: KindPortainer, Origin: OriginAttached, Status: TargetReady, Address: map[string]any{"tier": "lan"}, Ingress: map[string]any{"domain": "apps.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(t.Context(), target, "*", OfferCaps{}, "fixture"); err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, "lan", target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	return s, authority, dep, target
}

func TestDeploymentAddressEditsKeepGeneratedIdentityAndClaimsUntilRemoval(t *testing.T) {
	s, authority, dep, target := addressFixture(t)
	first, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	generated := "app-" + strings.ReplaceAll(dep.ID.String(), "-", "") + ".apps.example"
	if first.Ingress.URL != "https://"+generated || first.Revision != 1 {
		t.Fatalf("generated selection: %+v", first)
	}
	first.Ingress.Hosts[0] = "caller-mutated.example"
	if _, err := s.EditDeploymentLabel(t.Context(), dep.ProjectID, dep.ID, dep.DisplayLabel, "Customer release"); err != nil {
		t.Fatal(err)
	}
	target.Ingress["domain"] = "changed.example"
	if _, err := s.Targets.Put(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	selected, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 1, " Customer.Example. ")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Ingress.Hosts[0] != generated || selected.Ingress.URL != "https://customer.example" || selected.Ingress.CertificateDomain != "apps.example" || selected.Revision != 2 {
		t.Fatalf("mutable labels/target/caller changed generated routing: %+v", selected)
	}
	if _, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 1, "stale.example"); err == nil {
		t.Fatal("stale address edit accepted")
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 2, "unrecorded.example"); err == nil {
		t.Fatal("address receipt failure hidden")
	}
	q := deploymentsql.New(s.db.SQL())
	if _, err := q.LockHostnameClaim(t.Context(), "unrecorded.example"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed receipt left a reservation")
	}
	reading, err := s.ReadAddress(t.Context(), dep.ProjectID, dep.ID)
	if err != nil || reading.Revision != 2 || reading.CustomHostname != "customer.example" {
		t.Fatalf("failed edit changed desired settings: %+v %v", reading, err)
	}
	cleared, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	holders, err := q.HostnameClaimants(t.Context(), "customer.example")
	if err != nil || len(holders) != 1 || holders[0] != dep.ID {
		t.Fatalf("address edit freed possibly serving old hostname: %v %v", holders, err)
	}
	if cleared.Ingress.URL != "https://"+generated {
		t.Fatal("clearing custom address lost stable generated host")
	}
	authority.denial = errors.New("revoked")
	if _, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, cleared.Revision, "denied.example"); !errors.Is(err, authority.denial) {
		t.Fatalf("missing current authority refusal: %v", err)
	}
	authority.denial = nil
	settle := func() error {
		_, err := database.Transact(t.Context(), s.db, func(tx *database.Tx) ([]string, error) {
			return RemoveRecordedIn(t.Context(), tx, dep, false, RemovalSettlement{Target: target.Name, By: access.Principal{Name: "alice"}, At: time.Now()})
		})
		return err
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if err := settle(); err == nil {
		t.Fatal("failed removal receipt hidden")
	}
	if got, err := q.HostnameClaimants(t.Context(), "customer.example"); err != nil || len(got) != 1 {
		t.Fatalf("failed settlement freed claim: %v %v", got, err)
	}
	if err := settle(); err != nil {
		t.Fatal(err)
	}
	if got, err := q.HostnameClaimants(t.Context(), "customer.example"); err != nil || len(got) != 0 {
		t.Fatalf("confirmed settlement retained claim: %v %v", got, err)
	}
}

func TestConcurrentCustomerHostnameClaimHasOneOwner(t *testing.T) {
	s, _, first, target := addressFixture(t)
	project := createTestProject(t, s.db, "another-address", "Another address")
	second, err := s.Deployments.EnsureStanding(t.Context(), project, "lan", target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, dep := range []*Deployment{first, second} {
		go func() {
			ready.Done()
			<-start
			_, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, "shared.example")
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		}
	}
	holders, err := deploymentsql.New(s.db.SQL()).HostnameClaimants(t.Context(), "shared.example")
	if err != nil || success != 1 || len(holders) != 1 {
		t.Fatalf("concurrent claim: successes%d holders%v %v", success, holders, err)
	}
}

func TestCustomHostnameCannotEnterTheGeneratedNamespace(t *testing.T) {
	s, _, dep, _ := addressFixture(t)
	generated, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	reserved := "app-" + strings.ReplaceAll(ids.New(time.Now()).String(), "-", "") + ".example"
	if _, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, generated.Revision, reserved); err == nil {
		t.Fatal("custom hostname entered reserved generated namespace")
	}
}

func TestQueuedAddressSnapshotRefusesChangedOrConflictedReservations(t *testing.T) {
	s, _, dep, target := addressFixture(t)
	first, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, "first.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkAddressSnapshot(t.Context(), dep, target, &first); err != nil {
		t.Fatal(err)
	}
	if err := s.checkAddressSnapshot(t.Context(), dep, target, nil); err == nil {
		t.Fatal("missing pending address accepted")
	}
	target.Ingress["domain"] = "later.example"
	if err := s.checkAddressSnapshot(t.Context(), dep, target, &first); err != nil {
		t.Fatalf("mutable target changed admitted address: %v", err)
	}
	second, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, first.Revision, "second.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkAddressSnapshot(t.Context(), dep, target, &first); err == nil {
		t.Fatal("stale admitted address accepted")
	}
	if err := s.checkAddressSnapshot(t.Context(), dep, target, &second); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, s.db, "conflicting-address", "Conflicting address")
	other, err := s.Deployments.EnsureStanding(t.Context(), project, "lan", target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	q := deploymentsql.New(s.db.SQL())
	if err := q.AddHostnameClaimant(t.Context(), deploymentsql.AddHostnameClaimantParams{Hostname: "second.example", DeploymentID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.checkAddressSnapshot(t.Context(), dep, target, &second); err == nil {
		t.Fatal("conflicting admitted address accepted")
	}
}

func TestReleaseAddressParticipantRollsBackWithAdmission(t *testing.T) {
	s, _, dep, target := addressFixture(t)
	refused := errors.New("job enqueue refused")
	err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		if _, err := s.admitAddressIn(t.Context(), tx, dep, target, nil, nil, "alice"); err != nil {
			return err
		}
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatal(err)
	}
	reading, err := s.ReadAddress(t.Context(), dep.ProjectID, dep.ID)
	if err != nil || reading.Revision != 0 || len(reading.ReservedHosts) != 0 {
		t.Fatalf("failed admission retained address: %+v %v", reading, err)
	}
}

func TestNewAddressRequiresAValidTargetDomainWithoutChangingExistingRouting(t *testing.T) {
	s, _, dep, target := addressFixture(t)
	target.Ingress["domain"] = ""
	if _, err := s.Targets.Put(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, ""); err == nil {
		t.Fatal("new address accepted an empty target domain")
	}
	reading, err := s.ReadAddress(t.Context(), dep.ProjectID, dep.ID)
	if err != nil || reading.Revision != 0 || len(reading.ReservedHosts) != 0 {
		t.Fatalf("invalid target domain left an address: %+v %v", reading, err)
	}
	target.Ingress["domain"] = "apps.example"
	if _, err := s.Targets.Put(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	first, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	target.Ingress["domain"] = ""
	if _, err := s.Targets.Put(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	later, err := s.EditAddress(t.Context(), dep.ProjectID, dep.ID, first.Revision, "")
	if err != nil || !later.same(first) {
		t.Fatalf("target edit changed assigned address: %+v %v", later, err)
	}
}
