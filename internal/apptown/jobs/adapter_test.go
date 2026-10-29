package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestRiverAdapterStopsOneAttemptWorkOnShutdown(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	entered := make(chan struct{}, 2)
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{"model": {MaxAttempts: 1, Run: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(context.Background(), "model", "chat", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			if _, err := q.Client.JobRetry(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan error, 1)
		go func() { finished <- q.Run(ctx) }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not start")
		}
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not stop")
		}
		row, err := q.Client.JobGet(context.Background(), id)
		if err != nil || row.State != rivertype.JobStateCancelled || row.Attempt != attempt {
			t.Fatalf("shutdown: %+v %v", row, err)
		}
		if len(row.Errors) != attempt || !strings.Contains(row.Errors[attempt-1].Error, "explicit rerun required") {
			t.Fatalf("interruption not explained: %+v", row.Errors)
		}
	}
}

func TestRiverAdapterAdmissionPreservesPreciseInputsAndFailures(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{"work": {MaxAttempts: 1, Run: func(context.Context, string, map[string]any) (map[string]any, error) { return nil, nil }}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"generation": json.Number("9007199254740993")}
	id, err := q.Enqueue(context.Background(), "work", "same", values, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(context.Background(), "unknown", "same", nil, time.Time{}); err == nil {
		t.Fatal("unknown operation admitted")
	}
	if _, err := q.Enqueue(context.Background(), "work", "invalid", map[string]any{"bad": make(chan int)}, time.Time{}); err == nil {
		t.Fatal("invalid inputs admitted")
	}
	failNextJobInsert(t, db)
	if _, err := q.Enqueue(context.Background(), "work", "same", nil, time.Time{}); err == nil {
		t.Fatal("duplicate concealed failed admission")
	}
	again, err := q.Enqueue(context.Background(), "work", "same", map[string]any{"generation": 2}, time.Time{})
	if err != nil || again != id {
		t.Fatalf("duplicate: %d %v", again, err)
	}
	row, err := q.Client.JobGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var request jobs.Request
	if err := json.Unmarshal(row.EncodedArgs, &request); err != nil {
		t.Fatal(err)
	}
	if request.Values["generation"] != json.Number("9007199254740993") {
		t.Fatalf("generation lost precision: %+v; encoded %s; original %+v", request.Values, row.EncodedArgs, values)
	}
	if row.MaxAttempts != 1 || !row.ScheduledAt.After(time.Now()) {
		t.Fatalf("duplicate rewrote policy: %+v", row)
	}
}

func TestRiverQueueCancelledBeforeStartDoesNotTouchDatabase(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&jobs.Queue{}).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// A second periodic operation acts as a barrier: its execution proves the
// initial scheduling pass committed, so stopping cannot hide an unwanted backup.
func TestRiverPeriodicJobsDoNotRunOnRestart(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	for range 2 {
		tick := make(chan struct{}, 1)
		q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{
			"backup": {MaxAttempts: 1, Run: func(context.Context, string, map[string]any) (map[string]any, error) {
				return nil, errors.New("must wait for calendar")
			}},
			"clock": {MaxAttempts: 1, Run: func(context.Context, string, map[string]any) (map[string]any, error) {
				select {
				case tick <- struct{}{}:
				default:
				}
				return nil, nil
			}},
		}, nil,
			jobs.Schedule{Operation: "backup", Key: "daily", When: river.PeriodicInterval(time.Hour)},
			jobs.Schedule{Operation: "clock", Key: "tick", When: river.PeriodicInterval(100 * time.Millisecond)})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() { finished <- q.Run(ctx) }()
		select {
		case <-tick:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("periodic scheduler did not execute due work")
		}
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("periodic scheduler did not stop")
		}
		if backups := listedJobs(t, db, "backup"); len(backups) != 0 {
			t.Fatalf("restart enqueued a calendar job: %+v", backups)
		}
		var stray int
		// sql-guarantee: jobs.List reads only forge.operation rows; a calendar job enqueued under any other River kind must also be absent.
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM river_job WHERE args->>'operation'='backup'`).Scan(&stray); err != nil || stray != 0 {
			t.Fatalf("restart enqueued a calendar job: %d %v", stray, err)
		}
	}
}

// A PostgreSQL sequence advances even when the surrounding transaction rolls
// back, so precisely one real River INSERT fails without a library-specific
// production fault hook.
func failNextJobInsert(t *testing.T, db *database.DB) {
	t.Helper()
	queueExec(t, db, `CREATE SEQUENCE IF NOT EXISTS fail_job_insert_sequence`)
	// sql-guarantee: rewinds the fault sequence so exactly the next River insert fails; fault setup, not a read
	queueExec(t, db, `SELECT setval('fail_job_insert_sequence',1,false)`)
	queueExec(t, db, `CREATE OR REPLACE FUNCTION fail_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('fail_job_insert_sequence')=1 THEN RAISE EXCEPTION 'injected job insert failure'; END IF; RETURN NEW; END $$`)
	queueExec(t, db, `CREATE OR REPLACE TRIGGER fail_job_insert BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION fail_job_insert()`)
}
func queueExec(t *testing.T, db *database.DB, query string) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}

// listedJobs is every queued operation of one kind, read through the jobs
// page's own read; that read is bounded, so it refuses to count at the bound.
func listedJobs(t *testing.T, db *database.DB, operation string) []jobs.Job {
	t.Helper()
	const bound = 500
	all, err := jobs.List(context.Background(), db, bound)
	if err != nil || len(all) == bound {
		t.Fatalf("jobs %d: %v", len(all), err)
	}
	var out []jobs.Job
	for _, job := range all {
		if job.Kind == operation {
			out = append(out, job)
		}
	}
	return out
}

// Remediation and eval passes each run one at a time: a pass relies on no
// other pass of its kind running beside it (an eval arm found starting was
// interrupted). Kills: a second worker on either queue.
func TestPassQueuesRunOneJobAtATime(t *testing.T) {
	t.Parallel()
	for _, queue := range []string{jobs.RemediationQueue, jobs.EvalsQueue} {
		if n := jobs.QueueWorkers[queue]; n != 1 {
			t.Errorf("the %s queue runs %d at once", queue, n)
		}
	}
}
