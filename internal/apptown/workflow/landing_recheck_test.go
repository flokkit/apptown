package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type landingRetryBudgetFunc func(context.Context, *database.Tx, ids.ID, ids.ID, LandingRetry) error

func (f landingRetryBudgetFunc) ChargeIn(ctx context.Context, tx *database.Tx, p, c ids.ID, r LandingRetry) error {
	return f(ctx, tx, p, c, r)
}
func landingRecheckOwnerFixture(t *testing.T) (queries, *Run, ids.ID) {
	t.Helper()
	q, run := admitLandingSpec(t, schema.Spec{Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "scan", Uses: "scan"}}}}})
	db := q.db
	id := admittedStepID(t, q, run, "scan")
	attempts := newStepAttempts(db, func(kind string) bool { return kind == "scan" })
	step, err := readStep(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := attempts.Start(context.Background(), step, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if err := attempts.Finish(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"number":9007199254740993}`)}); err != nil {
		t.Fatal(err)
	}
	return q, run, id
}
func TestLandingRecheckBudgetAndResetCommitTogether(t *testing.T) {
	t.Parallel()
	q, run, id := landingRecheckOwnerFixture(t)
	q.db.Pool().SetMaxOpenConns(1)
	calls := 0
	owner := newLandingRechecks(q.db, []string{"scan"}, landingRetryBudgetFunc(func(ctx context.Context, tx *database.Tx, p, c ids.ID, retry LandingRetry) error {
		calls++
		if p != run.ProjectID || c != run.ChangeID || retry.ItemID != idstest.N(7) {
			t.Fatalf("budget scope %d %d %+v", p, c, retry)
		}
		return recordReceiptIn(ctx, tx, run.ID, "budget.claimed", struct {
			Item ids.ID `json:"item"`
		}{retry.ItemID}, time.Now())
	}))
	completionExec(t, q.db, `CREATE FUNCTION veto_landing_retry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='step.recheck' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
	completionExec(t, q.db, `CREATE TRIGGER veto_landing_retry BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_landing_retry()`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in := LandingRecheck{ChangeID: run.ChangeID, RunID: run.ID, who: "alice", Remediation: &LandingRetry{ItemID: idstest.N(7)}}
	if got, err := owner.Recheck(ctx, in); err == nil || len(got.Names) != 0 {
		t.Fatalf("suppressed receipt: %+v %v", got, err)
	}
	wantEventCount(t, q.db, 6, anyEvent)
	wantStepState(t, q.db, id, StatusFail)
	wantRetryRevision(t, q.db, run.ID, 0)
	completionExec(t, q.db, `DROP TRIGGER veto_landing_retry ON run_events`)
	got, err := owner.Recheck(ctx, in)
	if err != nil || len(got.Names) != 1 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	if got, err = owner.Recheck(ctx, in); err != nil || len(got.Names) != 0 {
		t.Fatalf("replay: %+v %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("replay charged budget: %d", calls)
	}
	wantEventCount(t, q.db, 1, ofKind("budget.claimed"))
	wantEventCount(t, q.db, 1, kindWith("step.recheck", "previousEvidence.number", "9007199254740993", "remediationItemId", idstest.N(7).String()))
	wantRetryRevision(t, q.db, run.ID, 1)
}
func TestLandingRecheckRechecksChangeProjectAfterLock(t *testing.T) {
	t.Parallel()
	q, run, id := landingRecheckOwnerFixture(t)
	other := workflowTestProject(t, q.db, "other", "Other", "")
	lock, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	holdProjectLock(t, lock, run.ProjectID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := newLandingRechecks(q.db, []string{"scan"}, nil).Recheck(ctx, LandingRecheck{ChangeID: run.ChangeID})
		done <- err
	}()
	dbtest.WaitForLock(t, q.db, "-- name: ProjectForUpdate")
	// sql-guarantee: Reparent change.project_id inside the held project-lock transaction after recheck starts waiting; Change transitions preserve ownership and no generated mutation reparents a change.
	if _, err = lock.SQL().ExecContext(context.Background(), `UPDATE changes SET project_id=$1,number=number+100000 WHERE id=$2`, other, run.ChangeID); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrRunMoved) {
		t.Fatalf("changed scope: %v", err)
	}
	wantStepState(t, q.db, id, StatusFail)
	wantEventCount(t, q.db, 6, anyEvent)
}
