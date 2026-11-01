package remediation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func outcomeFixture(t *testing.T) (*attemptScenario, outcomes, Item, StartDefinition) {
	t.Helper()
	f, starts, queued, def := attemptFixture(t, false)
	if run, err := starts.Start(context.Background(), queued, def, "worker"); err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	item := f.item(t, queued.ID)
	f.usage(t, usage.Entry{Kind: "step", ProjectID: item.ProjectID, RunID: item.RunID, RootRunID: item.RunID}, 100)
	return f, NewOutcomes(f.db, nil), item, def
}
func (f *attemptScenario) usage(t *testing.T, entry usage.Entry, cost int64) {
	t.Helper()
	if err := usage.New(f.db).Record(context.Background(), entry, usage.Totals{Total: 1, CostMicros: cost}, ""); err != nil {
		t.Fatal(err)
	}
}
func (f *attemptScenario) service(t *testing.T, review func(context.Context, Item) Review) *Service {
	t.Helper()
	return New(f.db, Dependencies{Work: f.work, Caller: f.ctx, Review: func(_ context.Context, item Item, _ VerificationScope) Review {
		return review(context.Background(), item)
	}})
}
func acceptedOutcomes(results []int) int {
	total := 0
	for _, n := range results {
		total += n
	}
	return total
}

func TestRemediationShippedChangeThatStillFailsBecomesStuck(t *testing.T) {
	t.Parallel()
	f, outcomes, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	verifier := newVerifier(outcomes, func(_ context.Context, got Item) Review {
		if got.ID != item.ID || got.ChangeID != item.ChangeID {
			t.Fatalf("verification scope: %+v", got)
		}
		return Review{ID: idstest.N(99), State: conformance.DoesNotConform, Detail: "the check still fails"}
	})
	if err := verifier.Verify(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	current := f.item(t, item.ID)
	if current.State != ItemStuck || current.VerifiedReviewID != idstest.N(99) || !strings.Contains(current.Reason, "still fails") {
		t.Fatalf("failed verification: %+v", current)
	}
}
func TestRemediationOutcomeRollsBackEveryRelatedRecord(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"stuck", "backoff", "landed", "verified"} {
		for _, failure := range []string{"-- name: FinishAttempt :execrows", "-- name: RollUpAttemptCost :exec", "-- name: RollUpItemCost :exec", "-- name: SetItemOutcome :execrows", "final audit"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				f, store, item, _ := outcomeFixture(t)
				result := Outcome{Kind: kind, Reason: "complete explanation", NotBefore: time.Now().Add(time.Hour), ReviewID: idstest.N(42), Verdict: conformance.Conforms}
				if kind == "landed" {
					item = f.outcome(t, item, Outcome{Kind: "stuck", Reason: "earlier failure"})
					f.moveChange(t, item, workflow.ShipChange)
					if err := NewAttempts(f.db, f.work, f.ctx, nil).close(context.Background(), item.RemediationID); err != nil {
						t.Fatal(err)
					}
				} else if kind == "verified" {
					f.moveChange(t, item, workflow.ShipChange)
					item = f.outcome(t, item, Outcome{Kind: "landed"})
				}
				before := f.snapshot(t)
				if failure == "final audit" {
					audit := map[string]string{"stuck": "stuck", "backoff": "backoff", "landed": "landed", "verified": "done"}[kind]
					// NOT VALID permits existing receipts; only the new final
					// item receipt fails, after a job reopening could have written.
					completionExec(t, f.db, `ALTER TABLE audit_log ADD CONSTRAINT reject_outcome CHECK (kind!='remediation.item.`+audit+`') NOT VALID`)
				} else {
					dbtest.FailNextStatement(t, f.db, failure)
				}
				if current, err := store.Record(context.Background(), item, result); err == nil || current != nil {
					t.Fatalf("failed transaction accepted: %+v %v", current, err)
				}
				if after := f.snapshot(t); after != before {
					t.Fatalf("partial outcome persisted\nbefore %s\nafter %s", before, after)
				}
			})
		}
	}
}

func TestRemediationOutcomeOnceAndStuckThenLandedAccounting(t *testing.T) {
	t.Parallel()
	f, store, item, _ := outcomeFixture(t)
	results := race(8, func(int) int {
		current, err := store.Record(context.Background(), item, Outcome{Kind: "stuck", Reason: "blocked"})
		if err != nil {
			t.Error(err)
		}
		if current != nil {
			return 1
		}
		return 0
	})
	if acceptedOutcomes(results) != 1 {
		t.Fatalf("accepted outcomes: %v", results)
	}
	oneItem(t, f.db, item.ID, func(i Item) bool { return i.State == ItemStuck && i.CostMicros == 100 })
	if n := attemptsWith(t, f.db, func(a Attempt) bool { return a.Outcome == "stuck" && a.CostMicros == 100 }); n != 1 {
		t.Fatalf("stuck attempts: %d", n)
	}
	receiptCount(t, f.db, 1, "remediation.item.stuck")
	item = f.item(t, item.ID)
	if err := (NewAttempts(f.db, nil, context.Background(), nil)).close(context.Background(), item.RemediationID); err != nil {
		t.Fatal(err)
	}
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	if item.CostMicros != 100 || item.State != itemVerifying {
		t.Fatalf("landing recounted the same spend: %+v", item)
	}
	oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemRunning && !j.FinishedAt.Valid })
	if n := attemptsWith(t, f.db, func(a Attempt) bool { return a.Outcome == "landed" && a.Reason == "" }); n != 1 {
		t.Fatalf("landed attempts: %d", n)
	}
	// Usage that arrived after the first accounting is included, once. The
	// root's own row has both run_id and root_run_id; it also counts only once.
	f.usage(t, usage.Entry{Kind: "step", ProjectID: item.ProjectID, RunID: idstest.N(999), RootRunID: item.RunID}, 25)
	item = f.outcome(t, item, Outcome{Kind: "verified", Verdict: conformance.DoesNotConform, ReviewID: idstest.N(42), Reason: "still fails"})
	if item.CostMicros != 125 || item.State != ItemStuck || item.VerifiedReviewID != idstest.N(42) {
		t.Fatalf("verification: %+v", item)
	}
	if n := attemptsWith(t, f.db, func(a Attempt) bool {
		return a.Outcome == "landed" && a.Reason == "still fails" && a.CostMicros == 125
	}); n != 1 {
		t.Fatalf("verified attempts: %d", n)
	}
	// This is a failed verification of work already known to have landed,
	// not a newly landed stuck attempt to re-review on every scheduler tick.
	service := f.service(t, func(context.Context, Item) Review {
		t.Error("repeated a completed verification")
		return Review{}
	})
	service.Pass(context.Background())
}

func TestRemediationOutcomeAccountingAcrossRetries(t *testing.T) {
	t.Parallel()
	f, store, item, def := outcomeFixture(t)
	item = f.outcome(t, item, Outcome{Kind: "stuck", Reason: "blocked"})
	oldRun := item.RunID
	starts := NewAttempts(f.db, f.work, f.ctx, nil)
	if err := starts.Retry(context.Background(), item.RemediationID, item.ID, "alice", "try again"); err != nil {
		t.Fatal(err)
	}
	queued := f.item(t, item.ID)
	if _, err := starts.Start(context.Background(), queued, def, "worker"); err != nil {
		t.Fatal(err)
	}
	current := f.item(t, item.ID)
	f.usage(t, usage.Entry{Kind: "step", ProjectID: item.ProjectID, RunID: current.RunID}, 20)
	f.usage(t, usage.Entry{Kind: "step", ProjectID: item.ProjectID, RunID: oldRun}, 10)
	f.usage(t, usage.Entry{Kind: "step", ProjectID: item.ProjectID, RunID: ids.Nil}, 9999)
	f.usage(t, usage.Entry{Kind: "step", ProjectID: ids.Nil, RunID: current.RunID}, 9999)
	current = f.outcome(t, current, Outcome{Kind: "stuck", Reason: "second result"})
	if current.CostMicros != 130 {
		t.Fatalf("total across attempts: %d", current.CostMicros)
	}
	histories, err := readAttempts(f.ctx, f.db, item.RemediationID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := histories[item.ID]
	if len(attempts) != 2 || attempts[0].CostMicros != 110 || attempts[1].CostMicros != 20 {
		t.Fatalf("attempt costs: %+v", attempts)
	}
	before := f.snapshot(t)
	if result, err := store.Record(context.Background(), item, Outcome{Kind: "stuck", Reason: "late old result"}); err != nil || result != nil {
		t.Fatalf("old attempt result: %+v %v", result, err)
	}
	if f.snapshot(t) != before {
		t.Fatal("old attempt changed accounting or history")
	}
}

func TestRemediationVerificationRejectsDelayedResults(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"new attempt", "same attempt new revision"} {
		t.Run(change, func(t *testing.T) {
			f, _, item, def := outcomeFixture(t)
			f.moveChange(t, item, workflow.ShipChange)
			item = f.outcome(t, item, Outcome{Kind: "landed"})
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var verifyErr error
			releaseReview := sync.OnceFunc(func() { close(release) })
			t.Cleanup(func() {
				releaseReview()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("review did not exit after release")
				}
			})
			var calls atomic.Int32
			v := newVerifier(NewOutcomes(f.db, nil), func(context.Context, Item) Review {
				calls.Add(1)
				close(entered)
				<-release
				return Review{ID: idstest.N(42), State: conformance.Conforms, Detail: "late success"}
			})
			go func() { verifyErr = v.Verify(context.Background(), item); close(done) }()
			select {
			case <-entered:
			case <-done:
				t.Fatalf("review returned before entering: %v", verifyErr)
			case <-time.After(10 * time.Second):
				t.Fatal("review did not enter")
			}
			// The local owner coalesces another request while this review runs.
			if err := v.Verify(context.Background(), item); err != nil {
				t.Error(err)
			}
			stuck := f.outcome(t, item, Outcome{Kind: "stuck", Reason: "intervening result"})
			if change == "new attempt" {
				starts := NewAttempts(f.db, f.work, f.ctx, nil)
				if err := starts.Retry(context.Background(), item.RemediationID, item.ID, "alice", "new attempt"); err != nil {
					t.Fatal(err)
				}
				queued := f.item(t, item.ID)
				if run, err := starts.Start(context.Background(), queued, def, "new-worker"); err != nil || run == nil {
					t.Fatalf("new start: %+v %v", run, err)
				}
			} else {
				f.outcome(t, stuck, Outcome{Kind: "landed"})
			}
			before := f.snapshot(t)
			releaseReview()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("delayed review did not finish")
			}
			if verifyErr != nil {
				t.Fatal(verifyErr)
			}
			if calls.Load() != 1 || f.snapshot(t) != before {
				t.Fatal("late review changed the current item or review was not coalesced")
			}
		})
	}
}

func TestRemediationVerificationWriteFailureRecovers(t *testing.T) {
	t.Parallel()
	f, _, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	before := f.snapshot(t)
	verifier := newVerifier(NewOutcomes(f.db, nil), func(context.Context, Item) Review {
		return Review{ID: idstest.N(42), State: conformance.Conforms}
	})
	dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
	if err := verifier.Verify(context.Background(), item); err == nil {
		t.Fatal("failed result write hidden")
	}
	if f.snapshot(t) != before {
		t.Fatal("failed verification result partially persisted")
	}
	var calls atomic.Int32
	restarted := f.service(t, func(context.Context, Item) Review {
		calls.Add(1)
		return Review{ID: idstest.N(43), State: conformance.Conforms}
	})
	restarted.Pass(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("recovery reviews: %d", calls.Load())
	}
	oneItem(t, f.db, item.ID, func(i Item) bool {
		return i.State == ItemDone && i.VerifiedReviewID == idstest.N(43) && i.CostMicros == 100
	})
	receiptCount(t, f.db, 1, "remediation.item.done")
	oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemCompleted })
}

func TestRemediationOutcomeLandedBeforeReviewAndJobCloseRace(t *testing.T) {
	t.Parallel()
	f, store, item, _ := outcomeFixture(t)
	item = f.outcome(t, item, Outcome{Kind: "stuck", Reason: "blocked"})
	f.moveChange(t, item, workflow.ShipChange)
	service := f.service(t, func(context.Context, Item) Review {
		t.Error("review ran before landing outcome committed")
		return Review{}
	})
	dbtest.FailNextStatement(t, f.db, "-- name: FinishAttempt :execrows")
	service.ReconcileItem(context.Background(), item)
	oneItem(t, f.db, item.ID, func(i Item) bool { return i.State == ItemStuck })
	race(2, func(n int) int {
		if n == 0 {
			if current, err := store.Record(context.Background(), item, Outcome{Kind: "landed"}); err != nil || current == nil {
				t.Errorf("landing: %+v %v", current, err)
			}
		} else if err := (NewAttempts(f.db, nil, context.Background(), nil)).close(context.Background(), item.RemediationID); err != nil {
			t.Error(err)
		}
		return 0
	})
	oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == RemRunning && !j.FinishedAt.Valid })
	oneItem(t, f.db, item.ID, func(i Item) bool { return i.State == itemVerifying })
}

func TestRemediationOutcomeSkipAcceptanceIncludesHistoryAndCost(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			f, _, item, _ := outcomeFixture(t)
			f.execute(t, item, workflow.StatusPass)
			if _, err := queries(f.db).SetItemState(f.ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: ItemAccepting}); err != nil {
				t.Fatal(err)
			}
			// Unknown approval evidence preservation is covered in Workflow;
			// this test covers the atomic withdrawal/history/accounting boundary.
			before := f.snapshot(t)
			if fail {
				dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
			}
			err := NewAttempts(f.db, f.work, f.ctx, nil).Skip(context.Background(), item.RemediationID, item.ID, "alice", "needs a different approach")
			if fail {
				if !errors.Is(err, dbtest.ErrInjectedFailure) || f.snapshot(t) != before {
					t.Fatal("failed skip was not atomic")
				}
				if steps := f.approvals(t, item.RunID); len(steps) != 1 || steps[0].Status != workflow.StatusPending {
					t.Fatalf("approval after failed skip: %+v", steps)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			oneItem(t, f.db, item.ID, func(i Item) bool { return i.State == ItemSkipped && i.CostMicros == 100 })
			if n := attemptsWith(t, f.db, func(a Attempt) bool {
				return a.Outcome == "skipped" && a.CostMicros == 100 && a.FinishedAt.Valid
			}); n != 1 {
				t.Fatalf("skipped attempts: %d", n)
			}
			steps := f.approvals(t, item.RunID)
			if len(steps) != 1 || steps[0].Status != workflow.StatusFail {
				t.Fatalf("withdrawn approval: %+v", steps)
			}
			ev := steps[0].Evidence
			if jsonText(t, ev, "withdrawal", "by") != "alice" || jsonText(t, ev, "withdrawal", "reason") != "needs a different approach" || jsonHas(t, ev, "decision") || jsonHas(t, ev, "approver") {
				t.Fatalf("withdrawal evidence: %s", ev)
			}
		})
	}
}

func TestRemediationOutcomeLandingPreservesPauseAndCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []string{RemPaused, RemCancelled} {
		t.Run(status, func(t *testing.T) {
			f, _, item, _ := outcomeFixture(t)
			item = f.outcome(t, item, Outcome{Kind: "stuck", Reason: "blocked"})
			f.moveChange(t, item, workflow.ShipChange)
			if err := NewAttempts(f.db, f.work, f.ctx, nil).status(context.Background(), item.RemediationID, []string{RemRunning}, status, "alice"); err != nil {
				t.Fatal(err)
			}
			f.outcome(t, item, Outcome{Kind: "landed"})
			oneJob(t, f.db, item.RemediationID, func(j Remediation) bool { return j.Status == status })
		})
	}
}

// A post-landing review the worker stopped judged nothing: the item stays
// verifying, and the next pass reviews it.
//
// Kills: a stopped review recorded as a verification outcome.
func TestAStoppedReviewLeavesTheItemVerifying(t *testing.T) {
	t.Parallel()
	f, _, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	stopped, cancel := context.WithCancel(context.Background())
	verifier := newVerifier(NewOutcomes(f.db, nil), func(ctx context.Context, _ Item) Review {
		cancel() // the worker stops during the review
		return Review{State: conformance.EvaluationError, Detail: ctx.Err().Error()}
	})
	if err := verifier.Verify(stopped, item); err == nil {
		t.Fatal("stopped review reported as verified")
	}
	if current := f.item(t, item.ID); current.State != itemVerifying {
		t.Fatalf("item after its review was stopped: %+v", current)
	}
	var calls atomic.Int32
	f.service(t, func(context.Context, Item) Review {
		calls.Add(1)
		return Review{ID: idstest.N(43), State: conformance.Conforms}
	}).Pass(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("next pass reviews: %d", calls.Load())
	}
	if current := f.item(t, item.ID); current.State != ItemDone || current.VerifiedReviewID != idstest.N(43) {
		t.Fatalf("item after the next pass: %+v", current)
	}
}
