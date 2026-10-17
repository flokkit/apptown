package remediation

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestRemediationCandidateWorkScopeAndFailedRead(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"shipped", "unshipped", "foreign", "missing", "active missing", "failed read"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			a, _, item, _ := outcomeFixture(t)
			a.execute(t, item, workflow.StatusFail)
			item = a.outcome(t, item, Outcome{Kind: "stuck", Reason: "tests failed"})
			want := 0
			if test != "unshipped" {
				a.moveChange(t, item, workflow.ShipChange)
			}
			switch test {
			case "shipped":
				want = 1
			case "unshipped":
			case "foreign":
				other := a.project(t, "other")
				// sql-guarantee: Rehome the shipped Change while preserving its original remediation item; no Change operation mutates its project identity.
				completionExec(t, a.db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, other, item.ChangeID)
			case "missing":
				// sql-guarantee: Remove only the shipped Change while retaining its item and history; project deletion would erase the orphan candidate under test.
				completionExec(t, a.db, `DELETE FROM changes WHERE id=?`, item.ChangeID)
			case "active missing":
				// sql-guarantee: Remove only the shipped Change while retaining its item and history; project deletion would erase the orphan candidate under test.
				completionExec(t, a.db, `DELETE FROM changes WHERE id=?`, item.ChangeID)
				requireStoredWrite(t)(queries(a.db).SetItemState(context.Background(), remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: ItemWorking, Reason: item.Reason}))
				want = 1
			case "failed read":
				dbtest.FailNextStatement(t, a.db, "JOIN changes c")
			}
			items, err := (NewReconciliation(a.db, a.work, a.ctx, nil)).Candidates()
			if test == "failed read" {
				if !errors.Is(err, database.ErrInjectedFailure) || items != nil {
					t.Fatalf("failed read became no candidates: %+v %v", items, err)
				}
				return
			}
			if err != nil || len(items) != want {
				t.Fatalf("candidates %+v: %v", items, err)
			}
		})
	}
}

func TestRemediationLandedWorkReadFailureRollsBack(t *testing.T) {
	t.Parallel()
	a, store, item, _ := outcomeFixture(t)
	a.moveChange(t, item, workflow.ShipChange)
	before := a.snapshot(t)
	dbtest.FailNextStatement(t, a.db, "JOIN changes c")
	out, err := store.Record(context.Background(), item, Outcome{Kind: "landed"})
	if !errors.Is(err, database.ErrInjectedFailure) || out != nil {
		t.Fatalf("failed landed observation: %+v %v", out, err)
	}
	if a.snapshot(t) != before {
		t.Fatal("failed Work observation changed remediation settlement")
	}
}
