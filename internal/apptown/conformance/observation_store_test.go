package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

func TestExceptionObservationReadFailuresAreNotExpiry(t *testing.T) {
	t.Parallel()
	store := reviewStore{db: testDB(t), checks: testCheckSource{}}
	dbtest.FailNextStatement(t, store.db, "FROM dispositions")
	if facts, err := store.current(context.Background()); err == nil || facts != nil {
		t.Fatalf("failed read became expiry: %v %v", facts, err)
	}
	if facts, err := store.current(context.Background()); err != nil || facts == nil || len(facts.Exceptions) != 0 {
		t.Fatalf("absent exception: %v %v", facts, err)
	}
}

// "stale" belongs to an observation, never to stored evaluator evidence.
// Both current and historical readers must reject this representation.
func TestReadersRejectViewOnlyPersistedOutcome(t *testing.T) {
	t.Parallel()
	db, pid := reviewFixture(t)
	store := reviewStore{db: db, checks: testCheckSource{}}
	ctx := context.Background()
	rev, err := store.create(ctx, store.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queries(db).InsertVerdict(ctx, conformancesql.InsertVerdictParams{ID: ids.New(stamp.Of(time.Now()).Time), ReviewID: rev.ID, ProjectID: pid, ProjectSlug: "reviewed", Sha: "fixed", Block: "hygiene", Version: 1, CheckName: "clean", Evaluator: "sh", Mode: "posture", State: "stale", CreatedAt: stamp.Of(time.Now()).Time})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.current(ctx); err == nil || got != nil {
		t.Fatalf("current accepted view-only evidence: %+v %v", got, err)
	}
	if got, err := store.detail(ctx, rev.ID); err == nil || got != nil {
		t.Fatalf("history accepted view-only evidence: %+v %v", got, err)
	}
}
