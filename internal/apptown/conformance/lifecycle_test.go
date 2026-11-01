package conformance

import (
	"context"
	"time"

	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/stamp"
)

func TestReviewTerminalRecordRejectsLateWrites(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	rev, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{ReviewID: rev.ID, ProjectID: id, ProjectSlug: "reviewed", SHA: "fixed", Block: "hygiene", Version: 1, Check: "clean", Evaluator: "sh", Mode: "posture", State: Conforms, At: stamp.Of(time.Now()).String()}
	if _, err := s.recordVerdict(context.Background(), verdictAdmission{Verdict: v}); err != nil {
		t.Fatal(err)
	}
	// The store derives the committed verdict count, even if a caller supplies a
	// stale progress snapshot; finalization is the authority for its own receipt.
	terminal, err := s.finish(context.Background(), *rev, ReviewDone)
	if err != nil || terminal.Verdicts != 1 {
		t.Fatalf("finish: %+v %v", terminal, err)
	}
	if err := s.progress(context.Background(), *rev); !errors.Is(err, errReviewClosed) {
		t.Fatalf("late progress: %v", err)
	}
	if _, err := s.recordVerdict(context.Background(), verdictAdmission{Verdict: v}); !errors.Is(err, errReviewClosed) {
		t.Fatalf("late verdict: %v", err)
	}
	if _, err := s.snapshotExceptions(context.Background(), rev.ID, id); !errors.Is(err, errReviewClosed) {
		t.Fatalf("late exception: %v", err)
	}
	if _, err := s.finish(context.Background(), *rev, ReviewError); !errors.Is(err, errReviewClosed) {
		t.Fatalf("second outcome: %v", err)
	}
	saved, err := readReview(context.Background(), s.db, rev.ID)
	if err != nil || !reflect.DeepEqual(saved, terminal) {
		t.Fatalf("terminal review changed: %+v %+v %v", saved, terminal, err)
	}
}

func TestReviewFinishSerializesWithVerdictAdmission(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	for range 8 {
		rev, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
		if err != nil {
			t.Fatal(err)
		}
		previous, err := s.latestVerdict(context.Background(), id, "hygiene", 1, "clean")
		if _, err := database.Lookup(err); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		inserted := make(chan error, 1)
		finished := make(chan struct {
			review *Review
			err    error
		}, 1)
		go func() {
			<-start
			_, err := s.recordVerdict(context.Background(), verdictAdmission{PreviousID: previous.ID, Verdict: Verdict{ReviewID: rev.ID, ProjectID: id, ProjectSlug: "reviewed", SHA: "fixed", Block: "hygiene", Version: 1, Check: "clean", Evaluator: "sh", Mode: "posture", State: Conforms, At: stamp.Of(time.Now()).String()}})
			inserted <- err
		}()
		go func() {
			<-start
			review, err := s.finish(context.Background(), *rev, ReviewDone)
			finished <- struct {
				review *Review
				err    error
			}{review, err}
		}()
		close(start)
		insertErr, outcome := <-inserted, <-finished
		if outcome.err != nil || (insertErr != nil && !errors.Is(insertErr, errReviewClosed)) {
			t.Fatalf("concurrent settlement: %v %v", insertErr, outcome.err)
		}
		count := 0
		if insertErr == nil {
			count = 1
		}
		if outcome.review.Verdicts != count {
			t.Fatalf("terminal count missed an admitted verdict: %+v inserted=%v", outcome.review, insertErr)
		}
		if held, err := queries(db).VerdictCountForReview(context.Background(), rev.ID); err != nil || held != int64(count) {
			t.Fatalf("verdicts held for the review: %d want %d: %v", held, count, err)
		}
	}
}

func TestReviewFailedFinishDoesNotReturnTerminalSnapshot(t *testing.T) {
	t.Parallel()
	s := reviewStore{db: testDB(t), checks: testCheckSource{}}
	rev, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, s.db, "SET finished_at")
	if result, err := s.finish(context.Background(), *rev, ReviewDone); err == nil || result != nil {
		t.Fatalf("uncommitted terminal state: %+v %v", result, err)
	}
	saved, err := readReview(context.Background(), s.db, rev.ID)
	if err != nil || saved.Status != ReviewRunning || !saved.FinishedAt.IsZero() {
		t.Fatalf("failed finish changed the record: %+v %v", saved, err)
	}
}

// An exception snapshot is one transaction: failure after the first insertion
// must not leave a partial historical lens. Root review orchestration separately
// verifies that snapshot errors become terminal review errors.
func TestReviewExceptionSnapshotRollsBackAsAWhole(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	createDispositionFixture(t, db, ExceptionRequest{ProjectID: id, Fingerprint: "second", Kind: AcceptedRisk, Justification: "fixture"})
	rev, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: trigger body that fails the second exception insert to prove snapshotExceptions rolls back; fault DDL, not a read
	completionExec(t, db, `CREATE FUNCTION reject_second_exception() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM conformance_review_exceptions)>0 THEN RAISE EXCEPTION 'second exception rejected'; END IF; RETURN NEW; END $$`)
	completionExec(t, db, `CREATE TRIGGER reject_second_exception BEFORE INSERT ON conformance_review_exceptions FOR EACH ROW EXECUTE FUNCTION reject_second_exception()`)
	if _, err := s.snapshotExceptions(context.Background(), rev.ID, id); err == nil {
		t.Fatal("second exception write should fail")
	}
	if held, err := queries(db).ReviewExceptionsForReview(context.Background(), rev.ID); err != nil || len(held) != 0 {
		t.Fatalf("partial exception snapshot: %+v %v", held, err)
	}
}
