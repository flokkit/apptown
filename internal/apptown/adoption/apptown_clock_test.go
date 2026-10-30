package adoption

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// What adoption stores is stamped by AppTown's clock, not the wall clock
// or the database's: on a clock ninety days behind, a lane selection's
// revision and an installed policy carry that clock's time.
func TestAppTownsClockStampsRevisionsAndPolicies(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "clocked", "Clocked")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "") }); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	if _, _, err := ways.PublishWorkflow(t.Context(), fmt.Sprintf(laneYAML, "picked", "picked", ""), "alice"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	s := New(db, Dependencies{Ways: ways, Now: func() time.Time { return at }})
	if err := s.SelectLane(t.Context(), pid, "picked", true, "alice"); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(t.Context(), pid, 10)
	if err != nil || len(history) != 1 || !history[0].CreatedAt.Time.Equal(at) {
		t.Fatalf("a revision stamped other than %s: %+v %v", at, history, err)
	}
	if err := s.InstallPolicy(t.Context(), pid, "scan-first", "", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	policies, err := ReadProjectPolicies(context.Background(), db, pid)
	if err != nil || len(policies) != 1 || policies[0].CreatedAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("a policy stamped other than %s: %+v %v", at, policies, err)
	}
}
