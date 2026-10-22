package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type retryScopeRecorder struct {
	retry func(context.Context, *database.Tx, ids.ID) (bool, error)
}

func (r retryScopeRecorder) RecordCompletionIn(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error) {
	return ReleaseReceipt{}, nil
}
func (r retryScopeRecorder) RetryCompletionIn(ctx context.Context, tx *database.Tx, id ids.ID) (bool, error) {
	return r.retry(ctx, tx, id)
}

func TestCompletionRetryRechecksTheAdmittedRunBeforeTheParticipant(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		"project", "change", "status", "stage", "revision", "parent attempt",
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
			expected, err := readRun(context.Background(), q.db, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "project":
				other := workflowTestProject(t, q.db, "other", "Other", "")
				// sql-guarantee: the stale completion snapshot must be fenced by changing only the stored project identity; no Workflow mutation exposes ownership reassignment.
				completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
			case "status":
				// sql-guarantee: Change only the completed run status after its snapshot; TransitionRun also changes UpdatedAt, an independent lockRunSnapshotIn mismatch that would mask this status fence.
				completionExec(t, q.db, `UPDATE runs SET status='running' WHERE id=?`, run.ID)
			case "stage":
				// sql-guarantee: Change only the completed run stage after its snapshot; TransitionRun also changes UpdatedAt and would mask a missing stage check in lockRunSnapshotIn.
				completionExec(t, q.db, `UPDATE runs SET current_stage='different' WHERE id=?`, run.ID)
			case "revision":
				// sql-guarantee: the stale completed-run snapshot must differ only in retry_revision; InvalidateRunDecision intentionally rejects completed runs.
				completionExec(t, q.db, `UPDATE runs SET retry_revision=retry_revision+1 WHERE id=?`, run.ID)
			case "parent attempt":
				changed, err := workflowsql.New(q.db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ParentStepAttempt: 1, ID: run.ID})
				if err != nil || changed != 1 {
					t.Fatalf("change parent attempt: changed=%d %v", changed, err)
				}
			case "change":
				// sql-guarantee: the stale completion snapshot must be fenced by clearing only its Change identity; no Workflow mutation exposes relationship reassignment.
				completionExec(t, q.db, `UPDATE runs SET change_id=NULL WHERE id=?`, run.ID)
			}
			calls := 0
			owner := newAdvancer(q.db, advanceRuntime{Completion: retryScopeRecorder{retry: func(context.Context, *database.Tx, ids.ID) (bool, error) { calls++; return true, nil }}})
			out, err := owner.AdvanceRun(context.Background(), expected, "alice")
			if !errors.Is(err, ErrRunMoved) || out != nil || calls != 0 {
				t.Fatalf("stale retry escaped: %+v %v calls=%d", out, err, calls)
			}
		})
	}
}

func TestCompletionRetryParticipantUsesTheGuardedTransaction(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		name := "commit"
		if fail {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
			q.db.Pool().SetMaxOpenConns(1)
			expected, err := readRun(context.Background(), q.db, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("retry participant failed")
			calls := 0
			recorder := retryScopeRecorder{retry: func(ctx context.Context, tx *database.Tx, id ids.ID) (bool, error) {
				calls++
				locked, err := readRun(ctx, tx, id)
				if err != nil {
					return false, err
				}
				if *locked != *expected {
					t.Errorf("participant received another Run: %+v", locked)
				}
				if err := recordReceiptIn(ctx, tx, id, "test.retry", struct{}{}, time.Now()); err != nil {
					return false, err
				}
				if fail {
					return false, failure
				}
				return true, nil
			}}
			owner := newAdvancer(q.db, advanceRuntime{Completion: recorder})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := owner.AdvanceRun(ctx, expected, "alice")
			if calls != 1 {
				t.Fatalf("participant calls=%d error=%v", calls, err)
			}
			if fail {
				if !errors.Is(err, failure) || out != nil {
					t.Fatalf("failed retry: %+v %v", out, err)
				}
				wantEventCount(t, q.db, 0, ofKind("test.retry"))
			} else {
				if err != nil || out == nil || out.Advanced || !out.FollowUpsPending || out.RunStatus != RunDone {
					t.Fatalf("retry: %+v %v", out, err)
				}
				wantEventCount(t, q.db, 1, ofKind("test.retry"))
			}
			wantRunState(t, q.db, run.ID, RunDone)
			wantEventCount(t, q.db, 0, ofKind("run.done"))
		})
	}
}
