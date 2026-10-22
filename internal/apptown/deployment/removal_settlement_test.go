package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

// A take-down's settlement removes only the placement selected before the
// provider was asked: one replaced under the same name meanwhile is refused
// and both its record and the new one's stay, with nothing retained or
// receipted.
//
// Kills: settling by the selected id without selecting by name again;
// settling by name without rechecking the placement.
func TestRemovalSettlementRefusesAReplacedPlacement(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	// What another door does while the provider is asked: the selected row
	// goes and a new deployment takes its name.
	if n, err := deploymentsql.New(s.db.SQL()).DeleteDeployment(context.Background(), dep.ID); err != nil || n != 1 {
		t.Fatalf("delete selected deployment: rows=%d err=%v", n, err)
	}
	replaced := configureForTest(t, s, ConfigureDeployment{ProjectID: dep.ProjectID, Name: dep.Name, Target: "cloud"})
	_, err := database.Transact(context.Background(), s.db, func(tx *database.Tx) ([]string, error) {
		return RemoveRecordedIn(context.Background(), tx, dep, false, RemovalSettlement{Target: "cloud", By: access.Principal{Name: "alice"}, At: time.Now()})
	})
	if !errors.Is(err, ErrRemovalChanged) {
		t.Fatalf("a replaced placement settled: %v", err)
	}
	if current, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); err != nil || current.ID != replaced.ID {
		t.Fatalf("the new deployment: %+v %v", current, err)
	}
	wantN(t, "retained resources", everyRetained(t, s.db), 0)
	wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down"), 0)
}

// The settlement is one transaction: its receipt failing leaves the
// placement, its need instances and nothing retained, and settling again
// succeeds.
//
// Kills: retaining or deleting outside the receipt's transaction.
func TestRemovalSettlementRollsBackTogether(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	dbtest.FailNextStatement(t, s.db, "INSERT INTO audit")
	_, err := database.Transact(context.Background(), s.db, func(tx *database.Tx) ([]string, error) {
		return RemoveRecordedIn(context.Background(), tx, dep, false, RemovalSettlement{Target: "cloud", By: access.Principal{Name: "alice"}, Taken: deploy.TakenDown{Left: []deploy.Left{{What: "a volume", Why: "kept"}}}, At: time.Now()})
	})
	if err == nil {
		t.Fatal("a settlement whose receipt failed committed")
	}
	if _, err := s.Deployments.ByID(t.Context(), dep.ID); err != nil {
		t.Fatalf("the placement went with a failed settlement: %v", err)
	}
	wantN(t, "retained resources", everyRetained(t, s.db), 0)
	wantN(t, "need instances", needInstances(t, s.db, dep.ID), 1)
	// The need instance and what the provider left are retained.
	if retained := settleRemoval(t, s, dep, "alice", deploy.TakenDown{Left: []deploy.Left{{What: "a volume", Why: "kept"}}}); len(retained) != 2 {
		t.Fatalf("settling again retained %v", retained)
	}
	wantN(t, "retained resources", everyRetained(t, s.db), 2)
}

// A restored copy's settlement settles only a restored copy: an ordinary
// deployment is refused and stays.
//
// Kills: settling any placement through the restored copy's door.
func TestRestoredSettlementRefusesAnOrdinaryDeployment(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	_, err := database.Transact(context.Background(), s.db, func(tx *database.Tx) ([]string, error) {
		return RemoveRestoredIn(context.Background(), tx, dep.ID, RemovalSettlement{Target: "cloud", By: access.Principal{Name: "alice"}, At: time.Now()})
	})
	if err == nil {
		t.Fatal("an ordinary deployment was settled as a restored copy")
	}
	if _, err := s.Deployments.ByID(t.Context(), dep.ID); err != nil {
		t.Fatalf("the deployment went: %v", err)
	}
	wantN(t, "torn-down receipts", receipts(t, s.db, "deployment.torn-down"), 0)
}
