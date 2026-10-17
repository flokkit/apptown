package remediation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/clock/clocktest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"
)

// Every time remediation stores is its owner's clock, through creation,
// an attempt's start, reconciliation's outcome and the job's automatic
// completion: with that clock a year ahead, each stored time reads a year
// ahead and an attempt's finish follows its start.
//
// Kills: any store built or used without the owner's clock, such as the
// outcome store reconciliation makes, or the attempts store automatic
// completion makes (its time reads as today).
func TestEveryRemediationTimeIsTheOwnersClock(t *testing.T) {
	t.Parallel()
	var ticks atomic.Int64
	base := time.Now().Add(365 * 24 * time.Hour)
	now := func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	f, _, _, def := attemptFixture(t, false)
	f.now = now
	items := f.job(t, 0, f.project(t, "clocked"))
	starts := NewAttempts(f.db, f.work, f.ctx, now)
	run, err := starts.Start(context.Background(), items[0], def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	item := f.item(t, items[0].ID)
	f.prepare(t, run.ID)
	f.execute(t, item, workflow.StatusFail)
	store := NewReconciliation(f.db, f.work, f.ctx, now)
	observed := observeRemediation(t, store, item)
	decision := Decide(observed)
	if decision.Action != ActionStuck {
		t.Fatalf("a failed attempt's decision: %+v", decision)
	}
	if applied, err := store.Apply(context.Background(), observed, decision); err != nil || applied == nil {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	if err := NewSchedule(f.db, now).CloseFinished(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := readJob(context.Background(), f.db, item.RemediationID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := readAttempts(context.Background(), f.db, item.RemediationID)
	if err != nil || len(attempts[item.ID]) != 1 {
		t.Fatalf("attempts: %+v %v", attempts, err)
	}
	attempt := attempts[item.ID][0]
	ahead := time.Now().Add(300 * 24 * time.Hour)
	stored := map[string]string{"the job's creation": job.CreatedAt, "the job's last change": job.UpdatedAt, "the item's last change": f.item(t, item.ID).UpdatedAt, "the attempt's start": attempt.StartedAt}
	if job.FinishedAt.Valid {
		stored["the job's finish"] = job.FinishedAt.Time.Format(time.RFC3339Nano)
	} else {
		t.Error("the job did not finish")
	}
	if attempt.FinishedAt.Valid {
		stored["the attempt's finish"] = attempt.FinishedAt.Time.Format(time.RFC3339Nano)
	} else {
		t.Error("the attempt did not finish")
	}
	for name, at := range stored {
		if parsed, err := time.Parse(time.RFC3339Nano, at); err != nil || !parsed.After(ahead) {
			t.Errorf("%s is %q, want the owner's clock, a year ahead: %v", name, at, err)
		}
	}
	if started, err := time.Parse(time.RFC3339Nano, attempt.StartedAt); err != nil || !attempt.FinishedAt.Time.After(started) {
		t.Errorf("the attempt finished at %s, not after its start %s: %v", attempt.FinishedAt.Time, attempt.StartedAt, err)
	}
}

// Each remediation operation takes one reading of its owner's clock for
// what it stores and for its receipt. On a clock that moves on a second at
// every reading, a receipt stamped by a separate reading says a later
// second than the record it describes: the job's creation, an attempt's
// start, reconciliation's outcome, a retry, a skip and a pool's size.
func TestARemediationReceiptCarriesItsRecordsTime(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewStepping(time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	f, _, _, def := attemptFixture(t, false)
	f.now = clk.Now
	latest := func(prefix string) (string, time.Time) {
		t.Helper()
		got, err := audit.Recent(context.Background(), f.db, audit.Filter{KindPrefix: prefix})
		if err != nil || len(got) == 0 {
			t.Fatalf("%s receipts: %+v %v", prefix, got, err)
		}
		at, err := time.Parse(time.RFC3339Nano, got[0].At)
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Kind, at
	}
	same := func(what, record string) {
		t.Helper()
		kind, receipt := latest("remediation.")
		at, err := time.Parse(time.RFC3339Nano, record)
		if err != nil || !at.Truncate(time.Second).Equal(receipt) {
			t.Errorf("%s stored at %s, its receipt (%s) at %v: %v", what, record, kind, receipt, err)
		}
	}

	items := f.job(t, 0, f.project(t, "stepping"))
	job, err := readJob(context.Background(), f.db, items[0].RemediationID)
	if err != nil {
		t.Fatal(err)
	}
	same("the job", job.CreatedAt)

	attempts := NewAttempts(f.db, f.work, f.ctx, clk.Now)
	run, err := attempts.Start(context.Background(), items[0], def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	item := f.item(t, items[0].ID)
	same("the attempt's start", item.UpdatedAt)

	f.prepare(t, run.ID)
	store := NewReconciliation(f.db, f.work, f.ctx, clk.Now)
	if _, err := store.Apply(context.Background(), observeRemediation(t, store, item), Decision{Action: ActionPhase, State: ItemAccepting, Reason: "moved on"}); err != nil {
		t.Fatal(err)
	}
	same("a phase", f.item(t, item.ID).UpdatedAt)

	item = f.item(t, item.ID)
	f.execute(t, item, workflow.StatusFail)
	observed := observeRemediation(t, store, item)
	if _, err := store.Apply(context.Background(), observed, Decide(observed)); err != nil {
		t.Fatal(err)
	}
	same("reconciliation's outcome", f.item(t, item.ID).UpdatedAt)

	if err := attempts.Retry(f.ctx, item.RemediationID, item.ID, "alice", "again"); err != nil {
		t.Fatal(err)
	}
	same("a retry", f.item(t, item.ID).UpdatedAt)

	if err := attempts.Skip(f.ctx, item.RemediationID, item.ID, "alice", "not now"); err != nil {
		t.Fatal(err)
	}
	same("a skip", f.item(t, item.ID).UpdatedAt)

	reading := clk.Next()
	if err := NewPools(f.db, clk.Now).Set(context.Background(), "remediation", 2, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, receipt := latest("workers.set"); !receipt.Equal(reading) {
		t.Errorf("a pool set at %v, its receipt at %v", reading, receipt)
	}
}
