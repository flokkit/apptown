package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

func repairQueue(t *testing.T, db *database.DB) *jobs.Queue {
	t.Helper()
	run := func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"executed": true}, nil
	}
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{"explicit": {Run: run, MaxAttempts: 1}, "retryable": {Run: run, MaxAttempts: 3}}, nil)
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
	return q
}

// Claim through River without launching a handler. A fetch may return only part
// of the available batch; verify the complete setup before testing repair.
// The integration tier separately proves that a killed real worker leaves this
// same persisted ownership state.
func claimRepairJobs(t *testing.T, db *database.DB, client string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor := riverdatabasesql.New(db.Pool()).GetExecutor()
	var claimed []int64
	for {
		available, err := executor.JobGetAvailable(ctx, &riverdriver.JobGetAvailableParams{ClientID: client, MaxAttemptedBy: 100, MaxToLock: 100, Queue: river.QueueDefault})
		if err != nil {
			t.Fatalf("claiming jobs for %q: %v (claimed %v; want %d)", client, err, claimed, want)
		}
		for _, row := range available.Jobs {
			claimed = append(claimed, row.ID)
		}
		if len(claimed) == want {
			return
		}
		if len(claimed) > want {
			t.Fatalf("River claimed unexpected jobs for %q: %v; want %d", client, claimed, want)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("claiming jobs for %q: %v (claimed %v; want %d)", client, ctx.Err(), claimed, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func settleJobs(t *testing.T, db *database.DB, client string) jobs.InterruptionRepair {
	t.Helper()
	out, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.InterruptionRepair, error) {
		return jobs.RepairInterruptedIn(context.Background(), tx, client, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInterruptedJobsPreserveRiverStateAndRemainExecutable(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q := repairQueue(t, db)
	ids := map[string]int64{}
	for _, key := range []string{"explicit", "retried", "retryable", "cancel-requested"} {
		op := "explicit"
		if key == "retryable" || key == "cancel-requested" {
			op = "retryable"
		}
		id, err := q.Enqueue(context.Background(), op, key, map[string]any{"runId": 123, "original": "keep"}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		ids[key] = id
	}
	claimRepairJobs(t, db, "earlier-owner", 4)
	// A prior real River cancellation/retry changes MaxAttempts, not the saved policy.
	prior := rivertype.AttemptError{At: time.Now().UTC().Truncate(time.Microsecond), Attempt: 1, Error: "previous failure"}
	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	_, err = riverdatabasesql.New(db.Pool()).GetExecutor().JobSetStateIfRunningMany(context.Background(), &riverdriver.JobSetStateIfRunningManyParams{
		ID: []int64{ids["retried"]}, Attempt: []*int{nil}, ErrData: [][]byte{data}, FinalizedAt: []*time.Time{&prior.At},
		MetadataDoMerge: []bool{false}, MetadataUpdates: [][]byte{nil}, ScheduledAt: []*time.Time{nil}, State: []rivertype.JobState{rivertype.JobStateCancelled},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Client.JobRetry(context.Background(), ids["retried"]); err != nil {
		t.Fatal(err)
	}
	claimRepairJobs(t, db, "dead-owner", 1)
	if _, err := q.Client.JobCancel(context.Background(), ids["cancel-requested"]); err != nil {
		t.Fatal(err)
	}
	before := map[string]*rivertype.JobRow{}
	for key, id := range ids {
		row, err := q.Client.JobGet(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		before[key] = row
	}
	// One connection also proves repair and audit use the caller's transaction.
	db.Pool().SetMaxOpenConns(1)
	started := time.Now().Add(-time.Second)
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{Settled: 4, Cancelled: 3, Available: 1}) {
		t.Fatal(got)
	}
	for key, id := range ids {
		row, err := q.Client.JobGet(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		old := before[key]
		if string(row.EncodedArgs) != string(old.EncodedArgs) || string(row.Metadata) != string(old.Metadata) || !reflect.DeepEqual(row.AttemptedBy, old.AttemptedBy) || !row.AttemptedAt.Equal(*old.AttemptedAt) || row.MaxAttempts != old.MaxAttempts || !slices.Equal(row.UniqueKey, old.UniqueKey) || !slices.Equal(row.UniqueStates, old.UniqueStates) {
			t.Fatalf("lost preserved fields: before=%+v after=%+v", old, row)
		}
		if key == "retryable" {
			if row.State != rivertype.JobStateAvailable || row.Attempt != 0 || row.FinalizedAt != nil || row.ScheduledAt.Before(started) || len(row.Errors) != 0 {
				t.Fatalf("refund: %+v", row)
			}
			again, err := q.Enqueue(context.Background(), "retryable", key, map[string]any{"original": "replaced"}, time.Time{})
			if err != nil || again != id {
				t.Fatalf("refund dedup: %d %v", again, err)
			}
		} else {
			if row.State != rivertype.JobStateCancelled || row.Attempt != old.Attempt || row.FinalizedAt == nil || row.FinalizedAt.Before(started) || !row.ScheduledAt.Equal(old.ScheduledAt) {
				t.Fatalf("cancellation: %+v", row)
			}
			if key == "cancel-requested" {
				if len(row.Errors) != 0 {
					t.Fatal(row.Errors)
				}
			} else {
				if len(row.Errors) != len(old.Errors)+1 {
					t.Fatal(row.Errors)
				}
				last := row.Errors[len(row.Errors)-1]
				if last.Attempt != row.Attempt || last.Error != "explicit rerun required: interrupted by shutdown" || last.At.Before(started) || last.Trace != "" {
					t.Fatalf("River cannot read expected attempt error: %+v", row.Errors)
				}
			}
		}
		entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled", Subject: strconv.FormatInt(id, 10)})
		if err != nil || len(entries) != 1 {
			t.Fatalf("receipt: %+v %v", entries, err)
		}
		var payload struct {
			JobID                                                          int64
			PreviousClient, CurrentClient, Operation, Key, Outcome, Reason string
			RetryOnInterrupt                                               bool
			AttemptBefore, AttemptAfter                                    int
		}
		if err := json.Unmarshal(entries[0].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.JobID != id || payload.CurrentClient != q.Client.ID() || payload.PreviousClient != old.AttemptedBy[len(old.AttemptedBy)-1] || payload.Key != key || payload.Outcome != string(row.State) || payload.AttemptBefore != old.Attempt || payload.AttemptAfter != row.Attempt || payload.Reason == "" || entries[0].Actor != "apptown-system" || !entries[0].ProjectID.IsZero() || strings.Contains(string(entries[0].Payload), "original") {
			t.Fatalf("receipt: %+v", entries)
		}
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{}) {
		t.Fatal(got)
	}
	entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled"})
	if err != nil || len(entries) != 4 {
		t.Fatalf("repeat receipt: %v %v", entries, err)
	}
	// River's own retry and worker must accept the appended errors, not only a SQL reader.
	if _, err := q.Client.JobRetry(context.Background(), ids["retried"]); err != nil {
		t.Fatal(err)
	}
	fresh, err := q.Enqueue(context.Background(), "explicit", "explicit", nil, time.Time{})
	if err != nil || fresh == ids["explicit"] {
		t.Fatalf("terminal dedup: %d %v", fresh, err)
	}
	probeStart(t, q.Client)
	for _, id := range []int64{ids["retryable"], ids["retried"], fresh} {
		probeAwaitState(t, q.Client, id, rivertype.JobStateCompleted)
	}
	row, err := q.Client.JobGet(context.Background(), ids["retried"])
	if err != nil || len(row.Errors) != 2 || !reflect.DeepEqual(row.Errors[0], prior) || !strings.Contains(row.Errors[1].Error, "explicit rerun required") {
		t.Fatalf("River execution lost errors: %+v %v", row, err)
	}
}

func TestInterruptedJobsRollbackOnInvalidOwnershipOrReceiptFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing owner", "invalid request", "missing key", "mutation", "receipt", "suppressed update", "caller", "empty current"} {
		t.Run(failure, func(t *testing.T) {
			db := databaseSuite.New(t)
			q := repairQueue(t, db)
			var ids []int64
			for _, key := range []string{"first", "second"} {
				id, err := q.Enqueue(context.Background(), "explicit", key, nil, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			claimRepairJobs(t, db, "dead-owner", 2)
			switch failure {
			case "missing owner":
				// sql-guarantee: an unidentifiable persisted owner must stop repair, not be skipped by SQL NULL.
				if _, err := db.Exec(`UPDATE river_job SET attempted_by='{}' WHERE id=?`, ids[1]); err != nil {
					t.Fatal(err)
				}
			case "invalid request":
				// sql-guarantee: malformed saved policy is unreachable through admission and must roll back earlier settlements.
				if _, err := db.Exec(`UPDATE river_job SET args=jsonb_set(args,'{retryOnInterrupt}','"invalid"') WHERE id=?`, ids[1]); err != nil {
					t.Fatal(err)
				}
			case "missing key":
				// sql-guarantee: an incomplete saved request cannot be guessed during repair.
				if _, err := db.Exec(`UPDATE river_job SET args=args-'key' WHERE id=?`, ids[1]); err != nil {
					t.Fatal(err)
				}
			case "mutation":
				defer db.FailNextStatement("-- name: SettleInterruptedOperation")()
			case "receipt":
				defer db.FailNextStatement("INSERT INTO audit_log")()
			case "suppressed update":
				if _, err := db.Exec(`CREATE FUNCTION suppress_repair() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TRIGGER suppress_repair BEFORE UPDATE ON river_job FOR EACH ROW EXECUTE FUNCTION suppress_repair()`); err != nil {
					t.Fatal(err)
				}
			}
			current := q.Client.ID()
			if failure == "empty current" {
				current = ""
			}
			var got jobs.InterruptionRepair
			err := db.InTx(context.Background(), func(tx *database.Tx) error {
				var err error
				got, err = jobs.RepairInterruptedIn(context.Background(), tx, current, time.Now())
				if err != nil {
					return err
				}
				if failure == "caller" {
					return errors.New("caller failed after repair")
				}
				return nil
			})
			if err == nil || (failure != "caller" && got != (jobs.InterruptionRepair{})) {
				t.Fatalf("failed repair: %+v %v", got, err)
			}
			if (failure == "missing owner" || failure == "invalid request" || failure == "missing key") && !strings.Contains(err.Error(), fmt.Sprint(ids[1])) {
				t.Fatalf("missing job identity: %v", err)
			}
			for _, id := range ids {
				row, err := q.Client.JobGet(context.Background(), id)
				if err != nil || row.State != rivertype.JobStateRunning || row.Attempt != 1 || len(row.Errors) != 0 {
					t.Fatalf("partial repair: %+v %v", row, err)
				}
			}
			receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled"})
			if err != nil || len(receipts) != 0 {
				t.Fatalf("partial receipts: %+v %v", receipts, err)
			}
		})
	}
}

func TestInterruptedJobsExcludeOtherOwnersKindsAndStates(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q := repairQueue(t, db)
	ctx := context.Background()
	add := func(key string, when time.Time) int64 {
		t.Helper()
		id, err := q.Enqueue(ctx, "explicit", key, nil, when)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	current := add("current", time.Time{})
	claimRepairJobs(t, db, q.Client.ID(), 1)
	foreignClient := probeClient(t, db.Pool(), nil)
	foreign, err := foreignClient.Insert(ctx, probeOperation{Key: "foreign"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := add("history", time.Time{})
	claimRepairJobs(t, db, "dead-owner", 2)
	// Pinned storage contract: only the final entry identifies the current owner.
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(ctx, &riverdriver.JobUpdateFullParams{ID: history, AttemptedByDoUpdate: true, AttemptedBy: []string{q.Client.ID(), "dead-owner"}}); err != nil {
		t.Fatal(err)
	}
	available := add("available", time.Time{})
	scheduled := add("scheduled", time.Now().Add(time.Hour))
	cancelled := add("cancelled", time.Time{})
	if _, err := q.Client.JobCancel(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	before := map[int64]*rivertype.JobRow{}
	for _, id := range []int64{current, foreign.Job.ID, available, scheduled, cancelled} {
		before[id], err = q.Client.JobGet(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{Settled: 1, Cancelled: 1}) {
		t.Fatal(got)
	}
	for id, old := range before {
		now, err := q.Client.JobGet(ctx, id)
		if err != nil || !reflect.DeepEqual(old, now) {
			t.Fatalf("excluded job changed: before=%+v after=%+v err=%v", old, now, err)
		}
	}
	probeAwaitState(t, q.Client, history, rivertype.JobStateCancelled)
}

func TestInterruptedJobsAbsentPolicyAndCancellationPresence(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q := repairQueue(t, db)
	absent, err := q.Enqueue(context.Background(), "retryable", "absent-policy", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := q.Enqueue(context.Background(), "retryable", "null-cancellation", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	claimRepairJobs(t, db, "dead-owner", 2)
	// sql-guarantee: historical requests without a saved flag must retain the decoder's false default, regardless of MaxAttempts.
	if _, err := db.Exec(`UPDATE river_job SET args=args-'retryOnInterrupt' WHERE id=?`, absent); err != nil {
		t.Fatal(err)
	}
	// Pinned River contract checks key presence, not the validity of its timestamp.
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: cancelled, MetadataDoUpdate: true, Metadata: []byte(`{"cancel_attempted_at":null}`)}); err != nil {
		t.Fatal(err)
	}
	if got := settleJobs(t, db, q.Client.ID()); got != (jobs.InterruptionRepair{Settled: 2, Cancelled: 2}) {
		t.Fatal(got)
	}
	for _, id := range []int64{absent, cancelled} {
		row, err := q.Client.JobGet(context.Background(), id)
		wantErrors := 0
		if id == absent {
			wantErrors = 1
		}
		if err != nil || row.State != rivertype.JobStateCancelled || row.Attempt != 1 || len(row.Errors) != wantErrors {
			t.Fatalf("saved policy: %+v %v", row, err)
		}
	}
}

func TestInterruptedJobsConcurrentRepairWritesOneReceiptPerJob(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q := repairQueue(t, db)
	for _, op := range []string{"explicit", "retryable"} {
		if _, err := q.Enqueue(context.Background(), op, op, nil, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	claimRepairJobs(t, db, "dead-owner", 2)
	type result struct {
		counts jobs.InterruptionRepair
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			counts, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.InterruptionRepair, error) {
				return jobs.RepairInterruptedIn(context.Background(), tx, q.Client.ID(), time.Now())
			})
			results <- result{counts, err}
		}()
	}
	close(start)
	var total jobs.InterruptionRepair
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		total.Settled += got.counts.Settled
		total.Cancelled += got.counts.Cancelled
		total.Available += got.counts.Available
	}
	if total != (jobs.InterruptionRepair{Settled: 2, Cancelled: 1, Available: 1}) {
		t.Fatal(total)
	}
	receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled"})
	if err != nil || len(receipts) != 2 {
		t.Fatalf("receipts: %+v %v", receipts, err)
	}
}

// An interrupted job is settled, and its receipt stamped, at the caller's
// time, however far from the machine's.
func TestAnInterruptedJobIsSettledAtTheCallersTime(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	q := repairQueue(t, db)
	if _, err := q.Enqueue(context.Background(), "explicit", "one", map[string]any{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	claimRepairJobs(t, db, "earlier-owner", 1)
	at := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (jobs.InterruptionRepair, error) {
		return jobs.RepairInterruptedIn(context.Background(), tx, q.Client.ID(), at)
	}); err != nil {
		t.Fatal(err)
	}
	lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled", Limit: 1})
	if err != nil || len(lines) != 1 || lines[0].At != at.Format(time.RFC3339) {
		t.Fatalf("receipt %+v %v, want it stamped %v", lines, err, at)
	}
}
