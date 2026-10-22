package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

func TestQueueReadAdapterPreservesReceiptsOrderingAndUnfinishedSelection(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	if rows, err := jobs.List(context.Background(), db, 0); err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("fresh queue: %v %v", err, rows)
	}
	active := insertJob(t, db, "complete", "open", idstest.N(7), rivertype.JobStateAvailable)
	insertJob(t, db, "other", "other", idstest.N(7), rivertype.JobStateAvailable)
	finished := insertJob(t, db, "complete", "finished", idstest.N(7), rivertype.JobStateCompleted)
	when := time.Date(2026, 9, 15, 1, 2, 3, 456000000, time.UTC)
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: active, AttemptedAtDoUpdate: true, AttemptedAt: &when, ErrorsDoUpdate: true, Errors: [][]byte{[]byte(`{"error":"earlier"}`), []byte(`{"error":"last error"}`)}, MetadataDoUpdate: true, Metadata: []byte(`{"output":{"receipt":9007199254740993}}`)}); err != nil {
		t.Fatal(err)
	}
	rows, err := jobs.List(context.Background(), db, 50)
	if err != nil || len(rows) != 3 || rows[2].ID != finished {
		t.Fatalf("queue ordering: %+v %v", rows, err)
	}
	var found bool
	for _, row := range rows {
		if row.ID != active {
			continue
		}
		found = true
		if row.Kind != "complete" || row.Key != "open" || row.State != "available" || row.Attempts != 1 || row.Error != "last error" || !row.StartedAt.Time.Equal(when) || row.FinishedAt.Valid || row.Receipt["receipt"] == nil {
			t.Fatalf("receipt: %+v", row)
		}
	}
	if !found {
		t.Fatal("active operation omitted")
	}
	one, err := jobs.List(context.Background(), db, 1)
	if err != nil || len(one) != 1 || one[0].ID == finished {
		t.Fatalf("limit: %+v %v", one, err)
	}
	err = db.InTx(context.Background(), func(tx *database.Tx) error {
		keys, err := jobs.UnfinishedKeysIn(context.Background(), tx.Native(), "complete")
		if err != nil {
			return err
		}
		if !slices.Equal(keys, []string{"open"}) {
			t.Fatalf("unfinished keys: %v", keys)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A corrupt later receipt must discard all rows, including valid earlier ones.
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: finished, MetadataDoUpdate: true, Metadata: []byte(`{"output":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if rows, err := jobs.List(context.Background(), db, 50); err == nil || rows == nil || len(rows) != 0 {
		t.Fatalf("corrupt receipt returned partial results: %+v %v", rows, err)
	}
}

// ByKey reads the newest job of one operation and key, with its last
// error: a newer pending job is pending though an older one ended, another
// key's failure is not this key's, a job whose attempts ended without
// succeeding says so with its error, and no job is not found (never ended).
func TestByKeyReadsTheNewestJobOfOneKey(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ctx := context.Background()
	insertJob(t, db, "complete", "r1/adopt", idstest.N(1), rivertype.JobStateDiscarded)
	insertJob(t, db, "complete", "r1/adopt", idstest.N(1), rivertype.JobStateAvailable)
	insertJob(t, db, "complete", "r1/cleanup", idstest.N(1), rivertype.JobStateDiscarded)
	ended := insertJob(t, db, "complete", "r2/adopt", idstest.N(2), rivertype.JobStateDiscarded)
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(ctx, &riverdriver.JobUpdateFullParams{ID: ended, ErrorsDoUpdate: true, Errors: [][]byte{[]byte(`{"error":"first"}`), []byte(`{"error":"the source changed"}`)}}); err != nil {
		t.Fatal(err)
	}
	if k, err := jobs.ByKey(ctx, db, "complete", "r1/adopt"); err != nil || k.State != "available" || k.Ended() {
		t.Fatalf("the newer pending job: %+v %v", k, err)
	}
	if k, err := jobs.ByKey(ctx, db, "complete", "r2/adopt"); err != nil || !k.Ended() || k.Error != "the source changed" {
		t.Fatalf("the ended job: %+v %v", k, err)
	}
	if _, err := jobs.ByKey(ctx, db, "complete", "r3/adopt"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no job: %v", err)
	}
}
