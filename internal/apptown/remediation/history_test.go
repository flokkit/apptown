package remediation

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/ids"
)

// What a check's past fixes cost is read from settled attempts only, for
// that block's check at any version, and says nothing with fewer than three:
// "no history" is never an estimate.
func TestAttemptHistoryNeedsThreeSettledAttempts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	p := createTestProject(t, db, "history", "History", "")
	job := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(time.Now()), Scope: "all", Actor: "alice", Status: "running"})
	settle := func(version int64, outcome string, cost int64, finish bool) {
		t.Helper()
		item := insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: p, ProjectSlug: "history", CheckKey: "k", Block: "hygiene", Version: version, CheckName: "clean", State: ItemDone}, 0, ids.Nil)
		// sql-guarantee: settled attempts with chosen costs and outcomes are the
		// history being read; producing three through real runs is not the guarantee.
		if err := queries(db).InsertAttempt(ctx, remediationsql.InsertAttemptParams{ID: ids.New(time.Now()), StartedAt: time.Now(), ItemID: item, N: 1}); err != nil {
			t.Fatal(err)
		}
		if finish {
			// sql-guarantee: the attempt's settlement, as the outcome store writes it.
			requireStoredWrite(t)(queries(db).FinishAttempt(ctx, remediationsql.FinishAttemptParams{At: time.Now(), ItemID: item, N: 1, Outcome: outcome, Outcome2: outcome}))
			// sql-guarantee: an attempt's cost; the roll-up derives it from another owner's usage records, which this read does not depend on.
			if _, err := db.SQL().ExecContext(context.Background(), `UPDATE remediation_attempts SET cost_micros=$1 WHERE item_id=$2`, cost, item); err != nil {
				t.Fatal(err)
			}
		}
	}
	settle(1, "landed", 2_000_000, true)
	settle(2, "stuck", 4_000_000, true)
	settle(1, "landed", 0, false) // still running: not history
	if h, err := AttemptHistoryIn(ctx, db, "hygiene", "clean"); err != nil || h != nil {
		t.Fatalf("two settled attempts are no history: %+v %v", h, err)
	}
	settle(3, "landed", 9_000_000, true)
	h, err := AttemptHistoryIn(ctx, db, "hygiene", "clean")
	if err != nil || h == nil || h.Attempts != 3 || h.MedianCost != 4 || h.SucceededShare < 0.66 || h.SucceededShare > 0.67 {
		t.Fatalf("three settled attempts at any version: %+v %v", h, err)
	}
	if other, err := AttemptHistoryIn(ctx, db, "hygiene", "other-check"); err != nil || other != nil {
		t.Fatalf("another check's history: %+v %v", other, err)
	}
}
