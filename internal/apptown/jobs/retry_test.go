package jobs_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

var databaseSuite = dbtest.NewSuite(func(_ context.Context, url string) (*database.DB, error) {
	db, err := dbschema.Open(context.Background(), url)
	if err != nil {
		return nil, err
	}
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
})

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

func insertJob(t *testing.T, db *database.DB, operation, key string, run ids.ID, state rivertype.JobState) int64 {
	t.Helper()
	var id int64
	err := db.InTx(context.Background(), func(tx *database.Tx) error {
		inserted, err := jobs.InsertOnTx(context.Background(), tx.Native(), "", jobs.Request{Operation: operation, Key: key, Values: map[string]any{"runId": run}}, 1, time.Time{})
		if err != nil {
			return err
		}
		id = inserted.Job.ID
		var finalized *time.Time
		switch state {
		case rivertype.JobStateCancelled, rivertype.JobStateDiscarded, rivertype.JobStateCompleted:
			now := time.Now().UTC()
			finalized = &now
		}
		_, err = riverdatabasesql.New(db.Pool()).UnwrapExecutor(tx.Native()).JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: id, AttemptDoUpdate: true, Attempt: 1, StateDoUpdate: true, State: state, FinalizedAtDoUpdate: true, FinalizedAt: finalized})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRetryRunOperationsPreservesSelectionAndRiverState(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	states := []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending, rivertype.JobStateCancelled, rivertype.JobStateDiscarded, rivertype.JobStateCompleted}
	for _, state := range states {
		insertJob(t, db, "complete", string(state), idstest.N(123), state)
	}
	insertJob(t, db, "other", "other", idstest.N(123), rivertype.JobStateCancelled)
	insertJob(t, db, "complete", "foreign", idstest.N(456), rivertype.JobStateDiscarded)
	db.Pool().SetMaxOpenConns(1)
	for pass := 0; pass < 2; pass++ {
		out, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.RetryResult, error) {
			return jobs.RetryRunOperationsIn(context.Background(), tx.Native(), jobs.RunOperations{Operation: "complete", RunID: idstest.N(123)})
		})
		want := jobs.RetryResult{Pending: true}
		if pass == 0 {
			want.Retried = 2
		}
		if err != nil || out != want {
			t.Fatalf("retry %d: %+v %v", pass, out, err)
		}
	}
	for _, test := range []struct {
		key      string
		state    rivertype.JobState
		attempts int
	}{
		{"cancelled", rivertype.JobStateAvailable, 2}, {"discarded", rivertype.JobStateAvailable, 2},
		{"completed", rivertype.JobStateCompleted, 1}, {"other", rivertype.JobStateCancelled, 1}, {"foreign", rivertype.JobStateDiscarded, 1},
	} {
		var state rivertype.JobState
		var attempts int
		// sql-guarantee: The retry must leave other jobs' max_attempts alone, and no owner read exposes max_attempts.
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT state,max_attempts FROM river_job WHERE args->>'key'=$1`, test.key).Scan(&state, &attempts); err != nil {
			t.Fatal(err)
		}
		if state != test.state || attempts != test.attempts {
			t.Fatalf("%s: %s attempts %d", test.key, state, attempts)
		}
	}
}

func TestRetryRunOperationsRollsBackWithCaller(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	id := insertJob(t, db, "complete", "a", idstest.N(123), rivertype.JobStateCancelled)
	failure := errors.New("required caller receipt failed")
	err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := jobs.RetryRunOperationsIn(context.Background(), tx.Native(), jobs.RunOperations{Operation: "complete", RunID: idstest.N(123)}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var state string
	for _, job := range listedJobs(t, db, "complete") {
		if job.ID == id {
			state = job.State
		}
	}
	if state != string(rivertype.JobStateCancelled) {
		t.Fatalf("state %q", state)
	}
}

func TestRetryRunOperationsRetainsLocksUntilCallerCommit(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	insertJob(t, db, "complete", "a", idstest.N(123), rivertype.JobStateCancelled)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	in := jobs.RunOperations{Operation: "complete", RunID: idstest.N(123)}
	if _, err := jobs.RetryRunOperationsIn(context.Background(), tx.Native(), in); err != nil {
		t.Fatal(err)
	}
	other, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback() }()
	if _, err := other.SQL().ExecContext(context.Background(), `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	lockedOut, lockedErr := jobs.RetryRunOperationsIn(context.Background(), other.Native(), in)
	var locked *pgconn.PgError
	if !errors.As(lockedErr, &locked) || locked.Code != "55P03" || lockedOut != (jobs.RetryResult{}) {
		t.Fatalf("selection did not retain lock: %+v %v", lockedOut, lockedErr)
	}
	if err := other.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	out, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.RetryResult, error) {
		return jobs.RetryRunOperationsIn(context.Background(), tx.Native(), in)
	})
	if err != nil || out != (jobs.RetryResult{Pending: true}) {
		t.Fatalf("after commit: %+v %v", out, err)
	}
}

func TestRetryRunOperationsRollsBackEarlierRetryWhenRiverFails(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	insertJob(t, db, "complete", "a", idstest.N(123), rivertype.JobStateCancelled)
	insertJob(t, db, "complete", "b", idstest.N(123), rivertype.JobStateDiscarded)
	if _, err := db.SQL().ExecContext(context.Background(), `ALTER TABLE river_job ADD CONSTRAINT reject_second_retry CHECK (args->>'key' <> 'b' OR max_attempts=1)`); err != nil {
		t.Fatal(err)
	}
	out, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.RetryResult, error) {
		return jobs.RetryRunOperationsIn(context.Background(), tx.Native(), jobs.RunOperations{Operation: "complete", RunID: idstest.N(123)})
	})
	if err == nil || out != (jobs.RetryResult{}) {
		t.Fatalf("failed retry: %+v %v", out, err)
	}
	var unchanged int
	// sql-guarantee: As above: a failed retry must not raise max_attempts, which no owner read exposes.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM river_job WHERE state IN ('cancelled','discarded') AND max_attempts=1`).Scan(&unchanged); err != nil || unchanged != 2 {
		t.Fatalf("unchanged %d: %v", unchanged, err)
	}
}
