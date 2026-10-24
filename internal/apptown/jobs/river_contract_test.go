// This is an executable adoption check, not a second production queue.
package jobs_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"

	"github.com/flokkit/apptown/internal/poll"
)

type probeOperation struct {
	Key   string `json:"key" river:"unique"`
	Value string `json:"value"`
}

func (probeOperation) Kind() string { return "forge_adoption_probe" }

type probeWorker struct {
	river.WorkerDefaults[probeOperation]
	run func(context.Context, *river.Job[probeOperation]) error
}

func (w *probeWorker) Work(ctx context.Context, job *river.Job[probeOperation]) error {
	return w.run(ctx, job)
}
func (w *probeWorker) NextRetry(*river.Job[probeOperation]) time.Time {
	return time.Now().Add(20 * time.Millisecond)
}

// River logs from concurrent services must survive a failed assertion without
// writing to testing.T after its test has returned.
type probeLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *probeLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *probeLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func probeClient(t *testing.T, db *sql.DB, run func(context.Context, *river.Job[probeOperation]) error) *river.Client[*sql.Tx] {
	t.Helper()
	return probeClientUsing(t, db, riverdatabasesql.New(db), run)
}

// configure, if given, adjusts the probe's River configuration before the
// client is made.
func probeClientUsing(t *testing.T, db *sql.DB, driver riverdriver.Driver[*sql.Tx], run func(context.Context, *river.Job[probeOperation]) error, configure ...func(*river.Config)) *river.Client[*sql.Tx] {
	t.Helper()
	var logs probeLog
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	workers := river.NewWorkers()
	river.AddWorker(workers, &probeWorker{run: run})
	config := &river.Config{
		Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		MaxAttempts: 1, JobTimeout: time.Minute, RescueStuckJobsAfter: time.Hour,
		PollOnly: true, FetchCooldown: time.Millisecond, FetchPollInterval: 10 * time.Millisecond,
		Logger: logger, TestOnly: true,
	}
	for _, f := range configure {
		f(config)
	}
	c, err := river.NewClient(driver, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("River client %s; database pool: %+v\n%s", c.ID(), db.Stats(), logs.String())
		}
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.StopAndCancel(ctx); err != nil {
			t.Error(err)
		}
	})
	return c
}

func probeStart(t *testing.T, c *river.Client[*sql.Tx]) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func probeInsert(t *testing.T, c *river.Client[*sql.Tx], args probeOperation, opts *river.InsertOpts) int64 {
	t.Helper()
	r, err := c.Insert(context.Background(), args, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r.Job.ID
}

func probeExecSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func probeCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func probeEventually(t *testing.T, description string, f func() bool) {
	t.Helper()
	// Leave headroom for a busy test database; success never waits for the deadline.
	if !poll.Until(context.Background(), 40*time.Second, 10*time.Millisecond, f) {
		t.Fatalf("timed out: %s", description)
	}
}

func probeAwaitState(t *testing.T, c *river.Client[*sql.Tx], id int64, state rivertype.JobState) *rivertype.JobRow {
	t.Helper()
	var row *rivertype.JobRow
	defer func() {
		if t.Failed() {
			t.Logf("client %s; last observed job: %+v", c.ID(), row)
		}
	}()
	probeEventually(t, fmt.Sprintf("job %d -> %s", id, state), func() bool {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		row, err = c.JobGet(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return row.State == state
	})
	return row
}

func probeAwaitShutdown(t *testing.T, c *river.Client[*sql.Tx], id int64, events <-chan *river.Event, state rivertype.JobState, kind river.EventKind) *rivertype.JobRow {
	t.Helper()
	var row *rivertype.JobRow
	var observed []*river.Event
	matched := false
	defer func() {
		if t.Failed() {
			t.Logf("client %s; shutdown row: %+v; want event %s", c.ID(), row, kind)
			for _, event := range observed {
				t.Logf("event %s: job=%+v stats=%+v", event.Kind, event.Job, event.JobStats)
			}
		}
	}()
	// State and event share the original forty-second observation budget.
	// The database assertion remains independent of the event's job snapshot.
	probeEventually(t, fmt.Sprintf("job %d -> %s and %s event", id, state, kind), func() bool {
		for events != nil {
			select {
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				observed = append(observed, event)
				if event.Job != nil && event.Job.ID == id && event.Kind == kind {
					matched = true
				}
			default:
				goto readRow
			}
		}
	readRow:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		row, err = c.JobGet(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return row.State == state && matched
	})
	return row
}

func TestRiverTransactionalEnqueue(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t).Pool()
	probeExecSQL(t, db, `CREATE TABLE proof_receipts (key text PRIMARY KEY,value text NOT NULL)`)
	var calls atomic.Int32
	c := probeClient(t, db, func(context.Context, *river.Job[probeOperation]) error { calls.Add(1); return nil })
	probeStart(t, c)
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// sql-guarantee: The synthetic domain receipt shares the enqueue transaction so rollback can prove admission atomicity; proof_receipts is a test-only table with no product owner or named query.
		if _, err := tx.Exec(`INSERT INTO proof_receipts VALUES ('accepted', 'done')`); err != nil {
			t.Fatal(err)
		}
		r, err := c.InsertTx(context.Background(), tx, probeOperation{Key: "accepted"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: This probes River's own commit-visibility contract from an outside connection; proof_receipts is a test-only table with no owner.
		if n := probeCount(t, db, `SELECT count(*) FROM river_job`); n != 0 {
			t.Fatalf("job visible before commit: %d", n)
		}
		// sql-guarantee: proof_receipts is a test-only table with no owner; the receipt must stay invisible outside the enqueue transaction until it commits
		if n := probeCount(t, db, `SELECT count(*) FROM proof_receipts`); n != 0 {
			t.Fatalf("receipt visible before commit: %d", n)
		}
		if calls.Load() != 0 {
			t.Fatal("executed uncommitted work")
		}
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			probeAwaitState(t, c, r.Job.ID, rivertype.JobStateCompleted)
		} else if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: proof_receipts is a test-only table created above with no owner; the commit must keep exactly one receipt beside the job
	if calls.Load() != 1 || probeCount(t, db, `SELECT count(*) FROM proof_receipts`) != 1 {
		t.Fatal("rollback/commit did not preserve atomic enqueue")
	}
}

func TestRiverUniqueKeyPreservesInputs(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t).Pool()
	c := probeClient(t, db, func(context.Context, *river.Job[probeOperation]) error { return nil })
	unique := river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}
	after := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	id := probeInsert(t, c, probeOperation{Key: "same", Value: "original"}, &river.InsertOpts{MaxAttempts: 1, ScheduledAt: after, UniqueOpts: unique})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r, err := c.Insert(context.Background(), probeOperation{Key: "same", Value: "replacement"}, &river.InsertOpts{MaxAttempts: 9, UniqueOpts: unique})
			if err != nil {
				t.Error(err)
				return
			}
			if r.Job.ID != id || !r.UniqueSkippedAsDuplicate {
				t.Errorf("duplicate created: %+v", r)
			}
			var args probeOperation
			if err := json.Unmarshal(r.Job.EncodedArgs, &args); err != nil {
				t.Error(err)
			}
			if args.Value != "original" || r.Job.MaxAttempts != 1 || !r.Job.ScheduledAt.Equal(after) {
				t.Errorf("duplicate changed original inputs: %+v", r.Job)
			}
		})
	}
	wg.Wait()
	finished := time.Now()
	if _, err := riverdatabasesql.New(db).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: id, StateDoUpdate: true, State: rivertype.JobStateCompleted, FinalizedAtDoUpdate: true, FinalizedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	if next := probeInsert(t, c, probeOperation{Key: "same"}, &river.InsertOpts{UniqueOpts: unique}); next == id {
		t.Fatal("completed job prevented an explicit new run")
	}
}

func TestRiverRetryPolicy(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t).Pool()
	c := probeClient(t, db, func(ctx context.Context, job *river.Job[probeOperation]) error {
		if job.Args.Key == "permanent" {
			return river.JobCancel(errors.New("invalid request"))
		}
		if job.Attempt == 1 {
			return errors.New("transient failure")
		}
		return nil
	})
	probeStart(t, c)
	one := probeInsert(t, c, probeOperation{Key: "one"}, nil)
	retry := probeInsert(t, c, probeOperation{Key: "retry"}, &river.InsertOpts{MaxAttempts: 2})
	permanent := probeInsert(t, c, probeOperation{Key: "permanent"}, &river.InsertOpts{MaxAttempts: 5})
	if row := probeAwaitState(t, c, one, rivertype.JobStateDiscarded); row.Attempt != 1 {
		t.Fatalf("one attempt: %+v", row)
	}
	if row := probeAwaitState(t, c, retry, rivertype.JobStateCompleted); row.Attempt != 2 {
		t.Fatalf("retry: %+v", row)
	}
	if row := probeAwaitState(t, c, permanent, rivertype.JobStateCancelled); row.Attempt != 1 {
		t.Fatalf("permanent: %+v", row)
	}
}

func TestRiverShutdownPolicy(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"default", "cancel_wrapping_context", "explicit_rerun", "cancel_wrapping_context_with_inflight_fetch"} {
		t.Run(policy, func(t *testing.T) {
			db := databaseSuite.New(t).Pool()
			entered := make(chan struct{})
			// A fetch already in flight can acquire the refunded job before the
			// producer stops. Signal first entry without panicking on redelivery.
			signalEntered := sync.OnceFunc(func() { close(entered) })
			var calls atomic.Int32
			var driver riverdriver.Driver[*sql.Tx] = riverdatabasesql.New(db)
			var fetch *shutdownFetchExecutor
			if policy == "cancel_wrapping_context_with_inflight_fetch" {
				fetch = &shutdownFetchExecutor{Executor: driver.GetExecutor(), entered: entered,
					held: make(chan struct{}), settled: make(chan struct{})}
				driver = shutdownFetchDriver{Driver: driver, executor: fetch}
			}
			c := probeClientUsing(t, db, driver, func(ctx context.Context, job *river.Job[probeOperation]) error {
				calls.Add(1)
				signalEntered()
				<-ctx.Done()
				if policy == "cancel_wrapping_context" || fetch != nil {
					return river.JobCancel(ctx.Err())
				}
				if policy == "explicit_rerun" {
					// Do not wrap context.Canceled: River treats that as a free retry
					// on probeClient stop, even inside JobCancel in this version.
					return river.JobCancel(errors.New("interrupted; explicit rerun required"))
				}
				return ctx.Err()
			})
			events, unsubscribe := c.Subscribe(river.EventKindJobInterrupted, river.EventKindJobCancelled, river.EventKindJobFailed, river.EventKindJobCompleted)
			defer unsubscribe()
			probeStart(t, c)
			id := probeInsert(t, c, probeOperation{Key: "shutdown"}, &river.InsertOpts{MaxAttempts: 1})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not start")
			}
			if fetch != nil {
				select {
				case <-fetch.held:
				case <-time.After(5 * time.Second):
					t.Fatal("no in-flight fetch after worker started")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.StopAndCancel(ctx); err != nil {
				t.Fatal(err)
			}
			if fetch != nil && calls.Load() != 2 {
				t.Fatalf("expected in-flight fetch to redeliver before shutdown; calls=%d", calls.Load())
			}
			if policy == "explicit_rerun" {
				row := probeAwaitShutdown(t, c, id, events, rivertype.JobStateCancelled, river.EventKindJobCancelled)
				if row.Attempt != 1 {
					t.Fatalf("lost attempt: %+v", row)
				}
			} else {
				row := probeAwaitShutdown(t, c, id, events, rivertype.JobStateAvailable, river.EventKindJobInterrupted)
				if row.Attempt != 0 {
					t.Fatalf("expected River's shutdown attempt refund: %+v", row)
				}
			}
			var replayed atomic.Int32
			restarted := probeClient(t, db, func(_ context.Context, job *river.Job[probeOperation]) error {
				if job.Args.Key == "shutdown" {
					replayed.Add(1)
				}
				return nil
			})
			probeStart(t, restarted)
			barrier := probeInsert(t, restarted, probeOperation{Key: "restart-probe"}, nil)
			probeAwaitState(t, restarted, barrier, rivertype.JobStateCompleted)
			if policy == "explicit_rerun" {
				probeAwaitState(t, restarted, id, rivertype.JobStateCancelled)
				if replayed.Load() != 0 {
					t.Fatal("explicit-rerun work replayed on restart")
				}
			} else {
				probeAwaitState(t, restarted, id, rivertype.JobStateCompleted)
				if replayed.Load() != 1 {
					t.Fatal("expected the shutdown-refunded job to execute again")
				}
			}
		})
	}
}

func TestRiverShutdownRemainsBounded(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t).Pool()
	entered, release := make(chan struct{}), make(chan struct{})
	c := probeClient(t, db, func(context.Context, *river.Job[probeOperation]) error {
		close(entered)
		<-release // deliberately uncooperative; a long job cannot block daemon exit
		return nil
	})
	t.Cleanup(func() { close(release) }) // unblock before probeClient cleanup
	probeStart(t, c)
	probeInsert(t, c, probeOperation{Key: "long"}, nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	if err := c.StopAndCancel(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("unbounded stop: %s", elapsed)
	}
	select {
	case <-c.Stopped():
		t.Fatal("stop claimed worker had exited")
	default:
	}
}

// failingMaintenanceStart makes River's maintenance fail to start: the
// periodic job enqueuer runs its start hooks as it starts, and an error from
// one fails that start. It fails the first `fail` starts and lets the rest
// through.
type failingMaintenanceStart struct {
	river.HookDefaults
	fail      int32
	calls     atomic.Int32
	recovered chan struct{}
}

func (h *failingMaintenanceStart) Start(context.Context, *rivertype.HookPeriodicJobsStartParams) error {
	n := h.calls.Add(1)
	if n <= h.fail {
		return fmt.Errorf("maintenance start %d refused", n)
	}
	if n == h.fail+1 {
		close(h.recovered)
	}
	return nil
}

// AppTown runs one poll-only River client. When its maintenance fails to
// start through a whole leadership term's retries, that client gives up the
// term, is elected again and starts maintenance again, without a restart:
// stuck jobs are rescued, scheduled jobs become available and periodic jobs
// are enqueued. Before River 0.48 the client asked to resign through a
// database notification, which a poll-only client never receives, and kept
// renewing a leadership whose maintenance had stopped.
//
// The rescuer looks every thirty seconds (River's interval, not settable),
// so the rescue below can take that long.
func TestRiverPollOnlyClientRestartsMaintenanceAfterItFailsToStart(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t).Pool()
	// River retries a failed start three times in one term before giving it up.
	hook := &failingMaintenanceStart{fail: 3, recovered: make(chan struct{})}
	var worked sync.Map
	c := probeClientUsing(t, db, riverdatabasesql.New(db), func(_ context.Context, job *river.Job[probeOperation]) error {
		worked.Store(job.Args.Key, true)
		return nil
	}, func(config *river.Config) {
		config.Hooks = []rivertype.Hook{hook}
		config.JobTimeout, config.RescueStuckJobsAfter = time.Second, 2*time.Second
		config.PeriodicJobs = []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) {
			return probeOperation{Key: "periodic"}, nil
		}, &river.PeriodicJobOpts{ID: "probe", RunOnStart: true})}
	})
	probeStart(t, c)
	select {
	case <-hook.recovered:
	case <-time.After(40 * time.Second):
		t.Fatalf("maintenance was not started again after %d failed starts", hook.calls.Load())
	}

	// Made after the recovered start, so only the recovered maintenance can
	// settle them. The stuck job is on a queue this client does not work, held
	// as running by a client that is gone, as a crashed AppTown leaves it.
	scheduled := probeInsert(t, c, probeOperation{Key: "scheduled"}, &river.InsertOpts{ScheduledAt: time.Now().Add(100 * time.Millisecond)})
	stuck := probeInsert(t, c, probeOperation{Key: "stuck"}, &river.InsertOpts{Queue: "gone"})
	claimed, err := riverdatabasesql.New(db).GetExecutor().JobGetAvailable(context.Background(), &riverdriver.JobGetAvailableParams{ClientID: "gone", MaxAttemptedBy: 100, MaxToLock: 1, Queue: "gone"})
	if err != nil || len(claimed.Jobs) != 1 || claimed.Jobs[0].ID != stuck {
		t.Fatalf("holding the stuck job: %v %+v", err, claimed)
	}

	probeAwaitState(t, c, scheduled, rivertype.JobStateCompleted)
	probeEventually(t, "the periodic job ran", func() bool { _, ok := worked.Load("periodic"); return ok })
	if row := probeAwaitState(t, c, stuck, rivertype.JobStateDiscarded); len(row.Errors) == 0 {
		t.Fatalf("the stuck job was settled without the rescuer's error: %+v", row)
	}
}
