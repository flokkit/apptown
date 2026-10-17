//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/ids/idstest"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/riverqueue/river/rivertype"
)

const crashProbeDSN = "FORGE_TEST_RIVER_CRASH_DSN"

// Establishes what a real process death leaves for Forge's startup repair.
// No expired wait context, fabricated running row, or shortened rescue horizon.
func TestIntegrationRiverProcessCrash(t *testing.T) {
	if dsn := os.Getenv(crashProbeDSN); dsn != "" {
		runCrashProbe(t, dsn)
		return
	}
	t.Parallel()
	db := databaseSuite.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	calls := make(chan string, 8)
	handler := func(_ context.Context, key string, _ map[string]any) (map[string]any, error) {
		calls <- key
		return nil, nil
	}
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{
		"explicit": {MaxAttempts: 1, Run: handler}, "retryable": {MaxAttempts: 3, Run: handler},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, operation := range []string{"explicit", "retryable"} {
		id, err := q.Enqueue(ctx, operation, operation, map[string]any{"runId": idstest.N(123).String()}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		ids[operation] = id
	}
	oldClient := killCrashWorker(t, db, []string{"explicit", "retryable"}, nil)
	if oldClient == q.Client.ID() {
		t.Fatal("restart reused the dead client identity")
	}
	for operation, id := range ids {
		row, err := q.Client.JobGet(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != rivertype.JobStateRunning || row.Attempt != 1 || len(row.AttemptedBy) != 1 || row.AttemptedBy[0] != oldClient {
			t.Fatalf("after process death: %+v", row)
		}
		var saved jobs.Request
		if err := json.Unmarshal(row.EncodedArgs, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.RetryOnInterrupt != (operation == "retryable") {
			t.Fatalf("wrong saved policy: %+v", saved)
		}
		again, err := q.Enqueue(ctx, operation, operation, map[string]any{"runId": idstest.N(123).String()}, time.Time{})
		if err != nil || again != id {
			t.Fatalf("stranded identity unexpectedly reusable: id=%d err=%v", again, err)
		}
		err = db.InTx(ctx, func(tx *database.Tx) error {
			out, err := jobs.RetryRunOperationsIn(ctx, tx.Native(), jobs.RunOperations{Operation: operation, RunID: idstest.N(123)})
			if err != nil {
				return err
			}
			if out != (jobs.RetryResult{Pending: true}) {
				return fmt.Errorf("running row unexpectedly retried: %+v", out)
			}
			keys, err := jobs.UnfinishedKeysIn(ctx, tx.Native(), operation)
			if err == nil && !slices.Contains(keys, operation) {
				return fmt.Errorf("running row lost unfinished ownership: %v", keys)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: killed owner %s, row=%s attempt=%d/%d retryOnInterrupt=%t; same-key enqueue deduplicates, explicit retry reports pending", operation, oldClient, row.State, row.Attempt, row.MaxAttempts, saved.RetryOnInterrupt)
	}
	probeStart(t, q.Client)
	t.Cleanup(func() {
		stop, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		if err := q.Client.StopAndCancel(stop); err != nil {
			t.Error(err)
		}
	})
	barrier, err := q.Enqueue(ctx, "explicit", "restart-barrier", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	probeAwaitState(t, q.Client, barrier, rivertype.JobStateCompleted)
	for operation, id := range ids {
		row, err := q.Client.JobGet(ctx, id)
		if err != nil || row.State != rivertype.JobStateRunning || row.Attempt != 1 {
			t.Fatalf("restart changed %s: %+v %v", operation, row, err)
		}
	}
	if got := <-calls; got != "restart-barrier" {
		t.Fatalf("restart replayed %q before rescue horizon", got)
	}
	select {
	case got := <-calls:
		t.Fatalf("unexpected replay: %s", got)
	default:
	}
	t.Log("new client completed fresh work while both dead-owner rows remained running")
}

// killCrashWorker returns only after the child has entered every requested
// operation and the operating system has confirmed its exit.
func killCrashWorker(t *testing.T, db *database.DB, keys []string, beforeKill func()) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Pool().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var dsn string
	err = conn.Raw(func(raw any) error { dsn = raw.(*stdlib.Conn).Conn().Config().ConnString(); return nil })
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIntegrationRiverProcessCrash$")
	child.Env = append(os.Environ(), crashProbeDSN+"="+dsn)
	var logs probeLog
	child.Stderr = &logs
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	decoder := json.NewDecoder(stdout)
	oldClient := ""
	seen := map[string]bool{}
	for range keys {
		var entered struct{ Key, Client string }
		if err := decoder.Decode(&entered); err != nil {
			t.Fatalf("child did not enter work: %v", err)
		}
		if !slices.Contains(keys, entered.Key) || seen[entered.Key] || entered.Client == "" {
			t.Fatalf("unexpected child entry: %+v", entered)
		}
		if oldClient != "" && oldClient != entered.Client {
			t.Fatal("child changed client identity")
		}
		oldClient = entered.Client
		seen[entered.Key] = true
	}
	if beforeKill != nil {
		beforeKill()
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("child exited normally instead of being killed")
	}
	return oldClient
}

func runCrashProbe(t *testing.T, dsn string) {
	t.Helper()
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var q *jobs.Queue
	run := func(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
		entry, err := json.Marshal(struct{ Key, Client string }{key, q.Client.ID()})
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintln(os.Stdout, string(entry)); err != nil {
			return nil, err
		}
		if key == "cancel-requested" {
			// An uncooperative worker cannot complete the cancellation before the
			// parent kills it. Only this disposable subprocess owns this goroutine.
			select {}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	q, err = jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{
		"explicit": {MaxAttempts: 1, Run: run}, "retryable": {MaxAttempts: 3, Run: run},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash worker returned before process termination")
}

func TestIntegrationRiverProcessCrashRepair(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ctx := context.Background()
	calls := make(chan jobs.Request, 4)
	handler := func(_ context.Context, key string, values map[string]any) (map[string]any, error) {
		calls <- jobs.Request{Key: key, Values: values}
		return nil, nil
	}
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{
		"explicit": {MaxAttempts: 1, Run: handler}, "retryable": {MaxAttempts: 3, Run: handler},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := q.Client.StopAndCancel(ctx); err != nil {
			t.Error(err)
		}
	})
	ids := map[string]int64{}
	for _, key := range []string{"explicit", "retryable", "cancel-requested"} {
		op := "retryable"
		if key == "explicit" {
			op = "explicit"
		}
		ids[key], err = q.Enqueue(ctx, op, key, map[string]any{"original": key}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
	}
	oldClient := killCrashWorker(t, db, []string{"explicit", "retryable", "cancel-requested"}, func() {
		if _, err := q.Client.JobCancel(ctx, ids["cancel-requested"]); err != nil {
			t.Fatal(err)
		}
	})
	for _, id := range ids {
		row, err := q.Client.JobGet(ctx, id)
		if err != nil || row.State != rivertype.JobStateRunning || row.Attempt != 1 || row.AttemptedBy[len(row.AttemptedBy)-1] != oldClient {
			t.Fatalf("not a confirmed interrupted job: %+v %v", row, err)
		}
	}
	if oldClient == q.Client.ID() {
		t.Fatal("current client is not new")
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{Settled: 3, Cancelled: 2, Available: 1}) {
		t.Fatal(got)
	}
	cancelled, err := q.Client.JobGet(ctx, ids["cancel-requested"])
	if err != nil || cancelled.State != rivertype.JobStateCancelled || len(cancelled.Errors) != 0 {
		t.Fatalf("cancellation precedence: %+v %v", cancelled, err)
	}
	// Retry increases MaxAttempts. A second real process death must still follow
	// the saved false flag, while the retryable job is refunded a second time.
	if _, err := q.Client.JobRetry(ctx, ids["explicit"]); err != nil {
		t.Fatal(err)
	}
	secondClient := killCrashWorker(t, db, []string{"explicit", "retryable"}, nil)
	if secondClient == oldClient || secondClient == q.Client.ID() {
		t.Fatal("client identity reused")
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{Settled: 2, Cancelled: 1, Available: 1}) {
		t.Fatal(got)
	}
	explicit, err := q.Client.JobGet(ctx, ids["explicit"])
	if err != nil || explicit.State != rivertype.JobStateCancelled || explicit.Attempt != 2 || explicit.MaxAttempts != 2 || len(explicit.Errors) != 2 {
		t.Fatalf("manual retry changed interruption policy: %+v %v", explicit, err)
	}
	for i, failure := range explicit.Errors {
		if failure.Attempt != i+1 || failure.Error != "explicit rerun required: interrupted by shutdown" {
			t.Fatal(explicit.Errors)
		}
	}
	retryable, err := q.Client.JobGet(ctx, ids["retryable"])
	if err != nil || retryable.State != rivertype.JobStateAvailable || retryable.Attempt != 0 || len(retryable.Errors) != 0 {
		t.Fatalf("retry refund: %+v %v", retryable, err)
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{}) {
		t.Fatal(got)
	}
	receipts, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "jobs.interruption-settled"})
	if err != nil || len(receipts) != 5 {
		t.Fatalf("repair receipts: %+v %v", receipts, err)
	}
	fresh, err := q.Enqueue(ctx, "explicit", "explicit", map[string]any{"original": "fresh"}, time.Time{})
	if err != nil || fresh == ids["explicit"] {
		t.Fatalf("explicit rerun admission: %d %v", fresh, err)
	}
	dedup, err := q.Enqueue(ctx, "retryable", "retryable", map[string]any{"original": "replacement"}, time.Time{})
	if err != nil || dedup != ids["retryable"] {
		t.Fatalf("retryable dedup: %d %v", dedup, err)
	}
	probeStart(t, q.Client)
	probeAwaitState(t, q.Client, fresh, rivertype.JobStateCompleted)
	probeAwaitState(t, q.Client, ids["retryable"], rivertype.JobStateCompleted)
	for range 2 {
		got := <-calls
		want := "retryable"
		if got.Key == "explicit" {
			want = "fresh"
		} else if got.Key != "retryable" {
			t.Fatalf("cancelled job replayed: %+v", got)
		}
		if got.Values["original"] != want {
			t.Fatalf("changed execution inputs: %+v", got)
		}
	}
	select {
	case extra := <-calls:
		t.Fatalf("unexpected replay: %+v", extra)
	default:
	}
}
