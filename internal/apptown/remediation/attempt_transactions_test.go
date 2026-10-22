package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestItemCommandsRequireTheirRecordedParentBeforeMutation(t *testing.T) {
	t.Parallel()
	f, store, item, _ := attemptFixture(t, true)
	if err := store.Failed(context.Background(), item, "ready for retry"); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	for _, run := range []func() error{
		func() error {
			return store.Retry(context.Background(), idstest.N(999999), item.ID, "alice", "retry")
		},
		func() error { return store.Skip(context.Background(), idstest.N(999999), item.ID, "alice", "skip") },
	} {
		if err := run(); !errors.Is(err, ErrItemScope) || fault.KindOf(err) != fault.NotFound {
			t.Fatalf("foreign parent: %v", err)
		}
		if f.snapshot(t) != before {
			t.Fatal("foreign parent changed item, job, attempt or receipt")
		}
	}
	// An item nobody recorded is out of scope too, never the store's own
	// absence error for a transport to interpret.
	for _, run := range []func() error{
		func() error {
			return store.Retry(context.Background(), item.RemediationID, idstest.N(1<<40), "alice", "retry")
		},
		func() error {
			return store.Skip(context.Background(), item.RemediationID, idstest.N(1<<40), "alice", "skip")
		},
	} {
		if err := run(); !errors.Is(err, ErrItemScope) || errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unknown item: %v", err)
		}
	}
	// A refusal the state explains is unprocessable, not a failure.
	if err := store.Skip(context.Background(), item.RemediationID, item.ID, "alice", " "); fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("skip without a reason: %v", err)
	}
	if f.snapshot(t) != before {
		t.Fatal("a refused command changed item, job, attempt or receipt")
	}
}

func TestRemediationRetryAtomicAndSafeAgainstJobCompletion(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"UPDATE remediations SET status", "INSERT INTO audit_log"} {
		t.Run(failure, func(t *testing.T) {
			f, store, item, _ := attemptFixture(t, true)
			if err := store.Failed(context.Background(), item, "retry feedback"); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(context.Background(), item.RemediationID); err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			dbtest.FailNextStatement(t, f.db, failure)
			if err := store.Retry(context.Background(), item.RemediationID, item.ID, "alice", "keep every word"); err == nil {
				t.Fatal("retry error hidden")
			}
			if f.snapshot(t) != before {
				t.Fatal("failed retry changed item, job, attempt or receipt")
			}
			oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemCompletedWithStuck && j.FinishedAt.Valid })
		})
	}
	f, store, item, _ := attemptFixture(t, true)
	if err := store.Failed(context.Background(), item, "retry feedback"); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("Keep the full feedback. ", 100)
	race(2, func(i int) int {
		var err error
		if i == 0 {
			err = store.Retry(context.Background(), item.RemediationID, item.ID, "alice", note)
		} else {
			err = store.Close(context.Background(), item.RemediationID)
		}
		if err != nil {
			t.Error(err)
		}
		return 0
	})
	oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemRunning && !j.FinishedAt.Valid })
	oneItem(t, f.db, item.ID, func(i Item) bool {
		return i.State == ItemQueued && i.Reason == "retry requested by alice: "+strings.TrimSpace(note)
	})
	kept := 0
	for _, r := range receipts(t, f.db, "remediation.item.retry") {
		if jsonText(t, r.Payload, "note") == note {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("retry receipts keeping the full note: %d", kept)
	}
}

func TestRemediationAttemptCancellationAndBusyGuards(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"paused", "cancelled", "skipped", "new feedback", "backoff", "running step", "landing", "pool paused"} {
		t.Run(change, func(t *testing.T) {
			f, store, item, def := attemptFixture(t, true)
			var err error
			switch change {
			case "paused":
				err = store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemPaused, "alice")
			case "cancelled":
				err = store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemCancelled, "alice")
			case "skipped":
				err = store.Skip(context.Background(), item.RemediationID, item.ID, "alice", "not needed")
			case "new feedback":
				if err = store.Failed(context.Background(), item, "new feedback"); err == nil {
					err = store.Retry(context.Background(), item.RemediationID, item.ID, "alice", "new feedback")
				}
			case "backoff":
				if run, startErr := store.Start(context.Background(), item, def, "worker"); startErr != nil || run == nil {
					t.Fatalf("start: %+v %v", run, startErr)
				}
				item = f.outcome(t, f.item(t, item.ID), Outcome{Kind: "backoff", Reason: "provider refused", NotBefore: time.Now().Add(time.Hour)})
			case "running step":
				f.retryExecution(t, item, workflow.StatusRunning)
			case "landing":
				f.moveChange(t, item, workflow.LandChange)
			case "pool paused":
				err = NewPools(f.db, nil).Set(context.Background(), WorkerKindRemediation, 0, "alice")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			if run, err := store.Start(context.Background(), item, def, "worker"); err != nil || run != nil {
				t.Fatalf("admission: %+v %v", run, err)
			}
			if f.snapshot(t) != before {
				t.Fatal("rejected admission changed remediation or receipts")
			}
		})
	}
	f, store, item, def := attemptFixture(t, true)
	race(2, func(i int) int {
		if i == 0 {
			if _, err := store.Start(context.Background(), item, def, "worker"); err != nil {
				t.Error(err)
			}
		} else if err := store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemCancelled, "alice"); err != nil {
			t.Error(err)
		}
		return 0
	})
	oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemCancelled })
	current := f.item(t, item.ID)
	if current.State == ItemSkipped {
		if state := f.changeState(t, item.ChangeID); state != workflow.ChangeInProgress {
			t.Fatalf("previous Change after cancelled start: %s", state)
		}
	} else if current.State != ItemWorking || current.Attempt != 2 {
		t.Fatalf("cancel/start outcome: %+v", current)
	}
	if err := store.Retry(context.Background(), item.RemediationID, item.ID, "alice", "retry"); err == nil {
		t.Fatal("cancelled job reopened")
	}
}

func TestRemediationAttemptPoolAdmissionIsSharedAcrossProjects(t *testing.T) {
	t.Parallel()
	f, store, item, def := attemptFixture(t, false)
	if err := NewPools(f.db, nil).Set(context.Background(), WorkerKindRemediation, 1, "alice"); err != nil {
		t.Fatal(err)
	}
	other := f.job(t, 0, f.project(t, "other"))[0]
	results := race(2, func(i int) int {
		snapshot := item
		if i == 1 {
			snapshot = other
		}
		run, err := store.Start(context.Background(), snapshot, def, "worker")
		if err != nil {
			t.Error(err)
		}
		if run != nil {
			return 1
		}
		return 0
	})
	if results[0]+results[1] != 1 {
		t.Fatalf("pool admissions: %v", results)
	}
	if n := itemsInState(t, f.db, ItemWorking); n != 1 {
		t.Fatalf("working items: %d", n)
	}
	if changes := f.changes(t); len(changes) != 1 {
		t.Fatalf("changes: %+v", changes)
	}
}

func TestRemediationOldWorkerCannotChangeNewAttempt(t *testing.T) {
	t.Parallel()
	f, store, item, def := attemptFixture(t, false)
	first, err := store.Start(context.Background(), item, def, "worker")
	if err != nil || first == nil {
		t.Fatalf("first attempt: %+v %v", first, err)
	}
	old := f.item(t, item.ID)
	f.execute(t, old, workflow.StatusFail)
	f.outcome(t, old, Outcome{Kind: "stuck", Reason: "failed"})
	if err := store.Retry(context.Background(), item.RemediationID, item.ID, "alice", "another attempt"); err != nil {
		t.Fatal(err)
	}
	second, err := store.Start(context.Background(), f.item(t, item.ID), def, "new-worker")
	if err != nil || second == nil {
		t.Fatalf("new attempt: %+v %v", second, err)
	}
	before := f.snapshot(t)
	if result, err := NewOutcomes(f.db, nil).Record(context.Background(), old, Outcome{Kind: "stuck", Reason: "late error"}); err != nil || result != nil {
		t.Fatalf("old worker result: %+v %v", result, err)
	}
	observed, err := NewReconciliation(f.db, f.work, f.ctx, nil).Observe(context.Background(), old)
	if err != nil || observed != nil {
		t.Fatalf("stale observation: %+v %v", observed, err)
	}
	if f.snapshot(t) != before {
		t.Fatal("old worker changed the new attempt")
	}
	if current := f.item(t, item.ID); current.State != ItemWorking || current.Attempt != 2 || current.RunID != second.ID || current.LeaseWorker != "new-worker" {
		t.Fatalf("new attempt: %+v", current)
	}
	if status := f.runStatus(t, first.ID); status != workflow.RunAbandoned {
		t.Fatalf("first run: %s", status)
	}
}

// Starting an attempt supersedes the previous Change and admits the new
// one in a single transaction. A failure anywhere in it (the new run, the
// attempt row, a silently suppressed summary of the old Change, the started
// receipt, or the commit itself) leaves the previous attempt exactly as it
// was and dispatches nothing; the same start then succeeds once the fault
// is gone. Root's TestRemediationAttemptRollsBackWithAppTownsCleanupJob covers
// AppTown cleanup job that joins this transaction.
func TestRemediationStartRollsBackSupersessionAndAllNewRecords(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"INSERT INTO runs", "INSERT INTO remediation_attempts", "suppressed summary", "last audit", "commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f, store, item, def := attemptFixture(t, true)
			before := f.snapshot(t)
			switch failure {
			case "commit":
				completionExec(t, f.db, `CREATE FUNCTION reject_attempt_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'attempt commit rejected'; END $$`)
				completionExec(t, f.db, `CREATE CONSTRAINT TRIGGER reject_attempt_commit AFTER INSERT ON remediation_attempts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_attempt_commit()`)
			case "suppressed summary":
				completionExec(t, f.db, `CREATE FUNCTION suppress_summary() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, f.db, `CREATE TRIGGER suppress_summary BEFORE UPDATE OF summary ON changes FOR EACH ROW EXECUTE FUNCTION suppress_summary()`)
			case "last audit":
				completionExec(t, f.db, `ALTER TABLE audit_log ADD CONSTRAINT reject_started CHECK (kind!='remediation.item.started') NOT VALID`)
			default:
				dbtest.FailNextStatement(t, f.db, failure)
			}
			var dispatched bool
			starter := NewStarter(store, func(context.Context, Item) (StartDefinition, error) { return def, nil }, func(ids.ID) error { dispatched = true; return nil })
			if _, err := starter.Start(context.Background(), item, "worker"); err == nil {
				t.Fatal("required failure was hidden")
			}
			if dispatched {
				t.Fatal("uncommitted attempt dispatched")
			}
			if state := f.changeState(t, item.ChangeID); state != workflow.ChangeInProgress {
				t.Fatalf("previous Change after a failed start: %s", state)
			}
			if status := f.runStatus(t, item.RunID); status != workflow.RunRunning {
				t.Fatalf("previous run after a failed start: %s", status)
			}
			if f.snapshot(t) != before {
				t.Fatal("failed start changed remediation, Work's records or receipts")
			}
			completionExec(t, f.db, `DROP FUNCTION IF EXISTS reject_attempt_commit() CASCADE`)
			completionExec(t, f.db, `DROP FUNCTION IF EXISTS suppress_summary() CASCADE`)
			completionExec(t, f.db, `ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS reject_started`)
			run, err := starter.Start(context.Background(), item, "worker")
			if err != nil || run == nil || !dispatched {
				t.Fatalf("explicit rerun: run=%+v dispatch=%v err=%v", run, dispatched, err)
			}
			if got := f.item(t, item.ID); got.State != ItemWorking || got.Attempt != 2 || got.RunID != run.ID {
				t.Fatalf("rerun item: %+v", got)
			}
			if status := f.runStatus(t, item.RunID); status != workflow.RunAbandoned {
				t.Fatalf("previous run after the rerun: %s", status)
			}
		})
	}
}

func TestRemediationResolutionFailureDoesNotSupersedeWork(t *testing.T) {
	t.Parallel()
	f, store, item, _ := attemptFixture(t, true)
	starter := NewStarter(store, func(context.Context, Item) (StartDefinition, error) {
		return StartDefinition{}, fmt.Errorf("workflow unavailable")
	}, func(ids.ID) error { t.Error("failed resolution dispatched"); return nil })
	if _, err := starter.Start(context.Background(), item, "worker"); err == nil {
		t.Fatal("resolution error hidden")
	}
	if state := f.changeState(t, item.ChangeID); state != workflow.ChangeInProgress {
		t.Fatalf("superseded Change: %s", state)
	}
	if current := f.item(t, item.ID); current.State != ItemStuck || current.Attempt != 1 || current.ChangeID != item.ChangeID || current.RunID != item.RunID {
		t.Fatalf("item after failed resolution: %+v", current)
	}
	if n := recordCounts(t, f.db).Attempts; n != 1 {
		t.Fatalf("attempts: %d", n)
	}
	receiptCount(t, f.db, 1, "remediation.item.start-failed")
}

func TestRemediationAttemptProjectAndJobLimits(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "job"} {
		t.Run(scope, func(t *testing.T) {
			f, store, item, def := attemptFixture(t, false)
			var other Item
			if scope == "project" {
				other = f.job(t, 0, item.ProjectID)[0]
			} else {
				// Creation freezes the entire scope; adding an item to a running job is
				// not a supported operation. Retire the unused single-project job first.
				if err := store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemCancelled, "alice"); err != nil {
					t.Fatal(err)
				}
				items := f.job(t, 1, item.ProjectID, f.project(t, "other"))
				item, other = items[0], items[1]
			}
			results := race(2, func(i int) int {
				snapshot := item
				if i == 1 {
					snapshot = other
				}
				run, err := store.Start(context.Background(), snapshot, def, "worker")
				if err != nil {
					t.Error(err)
				}
				if run != nil {
					return 1
				}
				return 0
			})
			if results[0]+results[1] != 1 {
				t.Fatalf("%s admissions: %v", scope, results)
			}
			if changes := f.changes(t); len(changes) != 1 {
				t.Fatalf("changes: %+v", changes)
			}
			if n := itemsInState(t, f.db, ItemQueued); n != 1 {
				t.Fatalf("queued items: %d", n)
			}
		})
	}
}

func TestRemediationAttemptBusyRetryDoesNotBlockOtherProjects(t *testing.T) {
	t.Parallel()
	f, _, item, _ := attemptFixture(t, true)
	f.retryExecution(t, item, workflow.StatusRunning)
	other := f.job(t, 0, f.project(t, "other"))[0]
	queue, err := NewSchedule(f.db, nil).Queue(context.Background())
	if err != nil || len(queue.Jobs) != 2 || len(queue.Jobs[0].Items) != 0 || len(queue.Jobs[1].Items) != 1 || queue.Jobs[1].Items[0].ID != other.ID {
		t.Fatalf("busy retry blocked a free project: %+v %v", queue, err)
	}
	if err := f.execution.Finish(workflow.StepOutcome{Status: workflow.StatusFail}, nil); err != nil {
		t.Fatal(err)
	}
	queue, err = NewSchedule(f.db, nil).Queue(context.Background())
	if err != nil || len(queue.Jobs) != 2 || len(queue.Jobs[0].Items) != 1 || len(queue.Jobs[1].Items) != 1 || queue.Jobs[0].Items[0].ID != item.ID || queue.Jobs[1].Items[0].ID != other.ID {
		t.Fatalf("blocked prior run prevented retry: %+v %v", queue, err)
	}
}

func TestRemediationControlsRollBackWithTheirReceipts(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"cancel", "skip", "pause", "close"} {
		t.Run(action, func(t *testing.T) {
			f, store, item, def := attemptFixture(t, false)
			if action == "skip" {
				if run, err := store.Start(context.Background(), item, def, "worker"); err != nil || run == nil {
					t.Fatalf("start: %+v %v", run, err)
				}
				item = f.item(t, item.ID)
				f.execute(t, item, workflow.StatusPass)
				// This is the owner's withdrawal transaction test: its phase
				// is the storage precondition, not a scheduler decision under test.
				if _, err := queries(f.db).SetItemState(f.ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: ItemAccepting}); err != nil {
					t.Fatal(err)
				}

			} else if action == "close" {
				if err := store.Failed(context.Background(), item, "failed resolution"); err != nil {
					t.Fatal(err)
				}
			}
			before := f.snapshot(t)
			dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
			var err error
			switch action {
			case "cancel":
				err = store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemCancelled, "alice")
			case "pause":
				err = store.Status(context.Background(), item.RemediationID, []string{RemRunning}, RemPaused, "alice")
			case "skip":
				err = store.Skip(context.Background(), item.RemediationID, item.ID, "alice", "no longer needed")
			case "close":
				err = store.Close(context.Background(), item.RemediationID)
			}
			if err == nil {
				t.Fatal("receipt failure hidden")
			}
			if f.snapshot(t) != before {
				t.Fatal("failed control changed state, history or receipts")
			}
			if action == "skip" {
				if steps := f.approvals(t, item.RunID); len(steps) != 1 || steps[0].Status != workflow.StatusPending || string(steps[0].Evidence) != "{}" {
					t.Fatalf("approval after failed skip: %+v", steps)
				}
			}
		})
	}
}

// A start the worker stopped while it resolved the definition assessed
// nothing: the item stays queued for the next pass, with no failed start.
//
// Kills: a stopped resolution recorded as a failed start.
func TestAStoppedResolutionLeavesTheItemQueued(t *testing.T) {
	t.Parallel()
	f, store, item, _ := attemptFixture(t, false)
	stopped, cancel := context.WithCancel(context.Background())
	starter := NewStarter(store, func(ctx context.Context, _ Item) (StartDefinition, error) {
		cancel() // the worker stops while the definition is resolved
		return StartDefinition{}, fmt.Errorf("reading the workflow: %w", ctx.Err())
	}, func(ids.ID) error { t.Error("stopped resolution dispatched"); return nil })
	if _, err := starter.Start(stopped, item, "worker"); err == nil {
		t.Fatal("stopped resolution reported as a start")
	}
	if current := f.item(t, item.ID); current.State != ItemQueued || current.Reason != "" {
		t.Fatalf("item after its resolution was stopped: %+v", current)
	}
}

// An attempt's outcome records work that happened: a worker stopping as it
// records it still leaves the outcome written.
//
// Kills: the outcome written on the worker's context.
func TestAnOutcomeIsRecordedThoughTheWorkerStopped(t *testing.T) {
	t.Parallel()
	f, starts, queued, def := attemptFixture(t, false)
	if run, err := starts.Start(context.Background(), queued, def, "worker"); err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	item := f.item(t, queued.ID)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if current, err := NewOutcomes(f.db, nil).Record(stopped, item, Outcome{Kind: "stuck", Reason: "failed"}); err != nil || current == nil || current.State != ItemStuck {
		t.Fatalf("outcome recorded by a stopped worker: %+v %v", current, err)
	}
}
