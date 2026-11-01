package remediation

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRemediationSummaryReadsAreBoundedAndChecked(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	fixes := readOnly(t, db)
	ctx := context.Background()
	pid := createTestProject(t, db, "summary", "Summary", "")
	counted := &countedQueries{Handle: db.SQL()}
	read := func(limit, want int) []Remediation {
		t.Helper()
		counted.reads = 0
		rows, err := readJobs(ctx, counted, limit)
		if err != nil || rows == nil || len(rows) != want || counted.reads != 1 {
			t.Fatalf("list: %d rows, %d reads: %v", len(rows), counted.reads, err)
		}
		return rows
	}
	read(50, 0)
	itemsByJob := map[ids.ID][]ids.ID{}
	for i := range 55 {
		created, err := time.Parse(time.RFC3339Nano, "2020-01-02T03:04:05.123456+11:00")
		if err != nil {
			t.Fatal(err)
		}
		job := insertStoredRemediation(t, db, remediationsql.InsertRemediationParams{ID: ids.New(created), Scope: "project", ScopeRef: "summary", Actor: "alice", Status: "running", CreatedAt: created, UpdatedAt: created.Add(time.Hour)})
		if i%2 == 0 {
			for _, state := range []string{ItemQueued, ItemWorking, ItemAccepting, ItemLanding, itemVerifying, ItemDone, ItemStuck, ItemSkipped, "future-state"} {
				id := insertStoredItem(t, db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: job, ProjectID: pid, ProjectSlug: "summary", CheckKey: "check", Block: "block", Version: 1, CheckName: "check", State: state}, 0, ids.Nil)
				itemsByJob[job] = append(itemsByJob[job], id)
				// Each sparse attempt has a distinct ledger attribution key; the job
				// projection reads the rolled-up item amount independently.
				if err := queries(db).InsertAttempt(ctx, remediationsql.InsertAttemptParams{ID: ids.New(time.Now()), StartedAt: time.Now(), ItemID: id, N: 1, RunID: id}); err != nil {
					t.Fatal(err)
				}
				if err := usage.New(db).Record(ctx, usage.Entry{Kind: "step", ProjectID: pid, RunID: id}, usage.Totals{Total: 1, CostMicros: 12345}, ""); err != nil {
					t.Fatal(err)
				}
				if err := queries(db).RollUpAttemptCost(ctx, id); err != nil {
					t.Fatal(err)
				}
				if err := queries(db).RollUpItemCost(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		if i == 0 {
			read(50, 1)
		}
	}
	rows := read(0, 50)
	read(-1, 50)
	read(5, 5)
	all := read(100, 55)
	for i, m := range all {
		if i > 0 && all[i-1].ID.Compare(m.ID) <= 0 {
			t.Fatal("not newest first")
		}
		counted.reads = 0
		one, err := readJob(ctx, counted, m.ID)
		if err != nil || counted.reads != 1 || !reflect.DeepEqual(*one, m) {
			t.Fatalf("point/list mismatch: %+v %+v %v", one, m, err)
		}
		if m.CreatedAt != "2020-01-01T16:04:05.123456Z" || m.UpdatedAt != "2020-01-01T17:04:05.123456Z" {
			t.Fatalf("timestamps: %+v", m)
		}
		wantSentence := "0 of 0 done"
		if i%2 == 0 {
			wantSentence = "1 of 9 done · 4 in progress · 1 queued · 1 stuck · 1 skipped"
			if m.Counts["total"] != 9 || m.Counts["future-state"] != 1 || m.Cost != 0.111105 {
				t.Fatalf("counts/cost: %+v", m)
			}
		} else if len(m.Counts) != 1 || m.Counts["total"] != 0 || m.Cost != 0 {
			t.Fatalf("empty job: %+v", m)
		}
		if m.Sentence != wantSentence || m.progressSentence() != wantSentence {
			t.Fatalf("sentence: %+v", m)
		}
	}
	if got, err := readJob(ctx, db, idstest.N(999999)); got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing: %+v %v", got, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range itemsByJob[rows[0].ID] {
		if err := usage.New(db).Record(ctx, usage.Entry{Kind: "step", ProjectID: pid, RunID: id}, usage.Totals{Total: 1, CostMicros: 1000000 - 12345}, ""); err != nil {
			t.Fatal(err)
		}
		requireStoredWrite(t)(queries(tx).SetItemState(ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: id, State: ItemDone}))
		if err := queries(tx).RollUpAttemptCost(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := queries(tx).RollUpItemCost(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	inside, err := readJob(ctx, tx, rows[0].ID)
	if err != nil || inside.Counts[ItemDone] != 9 || inside.Cost != 9 {
		t.Fatalf("transaction: %+v %v", inside, err)
	}
	outside, err := readJob(ctx, db, rows[0].ID)
	if err != nil || !reflect.DeepEqual(*outside, rows[0]) {
		t.Fatalf("transaction escaped: %+v %v", outside, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := readJobs(cancelled, db, 50); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "FROM remediations m CROSS JOIN LATERAL")
	if got, err := fixes.Remediations(ctx, 50); got != nil || err == nil {
		t.Fatalf("list failure hidden: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "FROM remediations m CROSS JOIN LATERAL")
	if got, err := fixes.Remediation(ctx, rows[0].ID); got != nil || err == nil {
		t.Fatalf("detail failure hidden: %+v %v", got, err)
	}
	if got, err := fixes.Remediation(ctx, idstest.N(999999)); got != nil || !errors.Is(err, errNoRemediation) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing job: %+v %v", got, err)
	}
}
