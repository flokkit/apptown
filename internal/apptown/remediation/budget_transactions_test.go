package remediation

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func TestLandingRetryBudgetRejectsChangedAttemptFacts(t *testing.T) {
	t.Parallel()
	for name, mutation := range map[string]func(*database.DB, Item){
		"state": func(db *database.DB, item Item) {
			requireStoredWrite(t)(queries(db).SetItemState(context.Background(), remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: ItemSkipped, Reason: item.Reason}))
		},
		"attempt": func(db *database.DB, item Item) {
			current, err := queries(db).ItemByID(context.Background(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			requireStoredWrite(t)(queries(db).StartItem(context.Background(), remediationsql.StartItemParams{UpdatedAt: time.Now(), ID: item.ID, LeaseWorker: current.LeaseWorker, LeaseUntil: current.LeaseUntil, ChangeID: item.ChangeID, RunID: item.RunID}))
			requireStoredWrite(t)(queries(db).SetItemState(context.Background(), remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason}))
		},
		"retries": func(db *database.DB, item Item) {
			current, err := queries(db).ItemByID(context.Background(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			requireStoredWrite(t)(queries(db).SetItemOutcome(context.Background(), remediationsql.SetItemOutcomeParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason, NotBefore: current.NotBefore, TransientRetries: 3, VerifiedReviewID: current.VerifiedReviewID, LeaseWorker: current.LeaseWorker, LeaseUntil: current.LeaseUntil}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, starts, item, definition := attemptFixture(t, false)
			run, err := starts.Start(context.Background(), item, definition, "worker")
			if err != nil || run == nil {
				t.Fatalf("start attempt: %+v %v", run, err)
			}
			item = f.item(t, item.ID)
			f.moveChange(t, item, workflow.LandChange)
			reconciliation := NewReconciliation(f.db, f.work, f.ctx, nil)
			observed := observeRemediation(t, reconciliation, item)
			applied, err := reconciliation.Apply(context.Background(), observed, Decide(observed))
			if err != nil || applied == nil {
				t.Fatalf("landing phase: %+v %v", applied, err)
			}
			item = f.item(t, item.ID)
			if item.State != ItemLanding {
				t.Fatalf("landing phase not recorded: %+v", item)
			}
			// ChargeIn is the owner-side compare-and-set boundary. Change exactly
			// one guarded fact after the caller took its retry snapshot. The named
			// writes also update bookkeeping fields absent from ChargeTransientRetry.
			retry := workflow.LandingRetry{ItemID: item.ID, Attempt: item.Attempt, Retries: item.TransientRetries, Reason: "provider overloaded"}
			mutation(f.db, item)
			before := outcomeSnapshot(t, f.db)
			_, err = database.Transact(context.Background(), f.db, func(tx *database.Tx) (struct{}, error) {
				return struct{}{}, (LandingRetryBudget{}).ChargeIn(context.Background(), tx, item.ProjectID, item.ChangeID, retry)
			})
			if err == nil {
				t.Fatal("changed retry facts accepted")
			}
			if outcomeSnapshot(t, f.db) != before {
				t.Fatal("rejected charge changed remediation state, receipts, or retry accounting")
			}
		})
	}
}
