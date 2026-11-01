package conformance

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"reflect"
	"testing"
)

func TestReviewStoreReturnsReceiptsOrErrors(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	empty, err := readConformanceReviews(context.Background(), s.db)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty reviews: %+v %v", empty, err)
	}
	first, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: ""}, "reviewer", ReviewRunning)
	if err != nil || first.Trigger != "manual" || first.Status != ReviewRunning {
		t.Fatalf("create: %+v %v", first, err)
	}
	second, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "scheduled"}, "system", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readReview(context.Background(), s.db, first.ID)
	if err != nil || !reflect.DeepEqual(first, got) {
		t.Fatalf("receipt changed on read: %+v %+v %v", first, got, err)
	}
	list, err := readConformanceReviews(context.Background(), s.db)
	if err != nil || len(list) != 2 || list[0].ID != second.ID || !reflect.DeepEqual(list[1], *got) {
		t.Fatalf("list/read disagree: %+v %v", list, err)
	}
	if got, err := readReview(context.Background(), s.db, idstest.N(999999)); got != nil || !errors.Is(err, errNoReview) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing review: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO conformance_reviews")
	if got, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning); err == nil || got != nil {
		t.Fatalf("failed write returned a receipt: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "FROM conformance_reviews")
	if got, err := readReview(context.Background(), s.db, first.ID); err == nil || got != nil {
		t.Fatalf("failed read returned a receipt: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "FROM conformance_reviews")
	if got, err := readConformanceReviews(context.Background(), s.db); err == nil || got != nil {
		t.Fatalf("failed list looks empty/successful: %+v %v", got, err)
	}
	wantHeld(t, "reviews", heldRecords(t, db).Reviews, 2)
}

func TestReviewStorageEnforcesOwnedStateTransitions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	r, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "alice", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.finish(context.Background(), *r, ReviewRunning); result != nil || !errors.Is(err, errReviewTransition) {
		t.Fatalf("nonterminal finish accepted: %+v %v", result, err)
	}
	saved, err := readReview(context.Background(), s.db, r.ID)
	if err != nil || saved.Status != ReviewRunning || saved.FinishedAt.Valid {
		t.Fatalf("refused transition mutated review: %+v %v", saved, err)
	}
	// Unknown persisted values fail at the owning decoder, before admission or
	// projection. Deliberately corrupt data is not another lifecycle state.
	if n, err := queries(db).FinishReview(context.Background(), conformancesql.FinishReviewParams{ID: r.ID, Status: "corrupt-review-state", Status_2: ReviewRunning.String()}); err != nil || n != 1 {
		t.Fatalf("corrupt status: rows=%d err=%v", n, err)
	}
	if result, err := readReview(context.Background(), s.db, r.ID); result != nil || !errors.Is(err, errReviewState) {
		t.Fatalf("corrupt review read: %+v %v", result, err)
	}
	if result, err := s.finish(context.Background(), *r, ReviewDone); result != nil || !errors.Is(err, errReviewState) {
		t.Fatalf("corrupt review settled: %+v %v", result, err)
	}
}
