package conformance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"

	"testing"
)

// These are saved-history fixtures, independent of App, Git or model execution.
func conformanceHistoryFixture(t *testing.T) (reviewStore, ids.ID, []ids.ID) {
	t.Helper()
	return conformanceHistoryFixtureWithOutcome(t, Conforms)
}

func conformanceHistoryFixtureWithOutcome(t *testing.T, latest Outcome) (reviewStore, ids.ID, []ids.ID) {
	t.Helper()
	db := testDB(t)
	projectID := createTestProject(t, db, "history", "History", "")
	reviews := []ids.ID{}
	for i := range 6 {
		status := ReviewDone
		if i == 1 || i == 4 {
			status = ReviewError
		}
		if i == 2 {
			status = ReviewRunning
		}
		number, err := queries(db).NextReviewNumber(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r, err := queries(db).InsertReview(context.Background(), conformancesql.InsertReviewParams{ID: ids.New(stamp.Of(time.Now()).Time), Number: number, Trigger: "manual", Actor: "alice", StartedAt: stamp.Of(time.Now()).Time, Status: status.String(), SelectedProjects: "null"})
		if err != nil {
			t.Fatal(err)
		}
		rid := r.ID
		state := DoesNotConform
		if i == 5 {
			state = latest
		}
		if _, err := queries(db).InsertVerdict(context.Background(), conformancesql.InsertVerdictParams{ID: ids.New(stamp.Of(time.Now()).Time), ReviewID: rid, ProjectID: projectID, ProjectSlug: "history", Sha: fmt.Sprintf("sha-%d", i), Block: "standard", Version: 1, CheckName: "clear", Evaluator: "llm", EvaluatorVer: "v1", Mode: "posture", State: state.String(), Severity: "note", CreatedAt: stamp.Of(time.Now()).Time}); err != nil {
			t.Fatal(err)
		}
		reviews = append(reviews, rid)
	}
	return reviewStore{db: db, checks: testCheckSource{}}, projectID, reviews
}

func TestReviewQueryPagesKeepComparisonsAndBoundQueries(t *testing.T) {
	t.Parallel()
	s, projectID, reviews := conformanceHistoryFixture(t)
	db := &countedQueries{Handle: s.db.SQL()}
	for _, limit := range []int{1, 4, 100} {
		db.reads = 0
		summaries, err := readReviewSummaries(context.Background(), db, limit)
		if err != nil || len(summaries) != min(limit, len(reviews)) {
			t.Fatalf("page %d: %+v %v", limit, summaries, err)
		}
		d := summaries[0].Delta
		if d == nil || d.Previous != reviews[3] || d.Fixed != 1 || d.ComparedProjects != 1 {
			t.Fatalf("page %d lost its earlier completed review: %+v", limit, d)
		}
		if db.reads != 3 {
			t.Fatalf("page %d used %d queries; want three regardless of page size", limit, db.reads)
		}
		for _, r := range summaries {
			if r.Status != ReviewDone && r.Delta != nil {
				t.Fatalf("unfinished/failed review compared: %+v", r)
			}
		}
	}
	db.reads = 0
	history, err := readProjectHistory(context.Background(), db, projectID, 2)
	if err != nil || len(history) != 2 || history[0].ReviewID != reviews[4] || history[1].ReviewID != reviews[5] || history[0].Flagged != 1 || history[1].Standing != StandingConforms {
		t.Fatalf("recent project history: %+v %v", history, err)
	}
	if db.reads != 3 {
		t.Fatalf("history used %d queries; want three", db.reads)
	}
	latest, err := s.latestStanding(context.Background(), projectID, "renamed")
	if err != nil || latest == nil || latest.ReviewID != reviews[5] || latest.Standing != history[1].Standing || latest.Slug != "renamed" {
		t.Fatalf("latest standing disagrees with history: %+v %v", latest, err)
	}
}

func TestReviewQueriesPropagateFailuresAndCancellation(t *testing.T) {
	t.Parallel()
	s, projectID, reviews := conformanceHistoryFixture(t)
	ctx := context.Background()
	for _, fragment := range []string{"FROM conformance_reviews", "FROM conformance_verdicts", "FROM conformance_adjudications"} {
		dbtest.FailNextStatement(t, s.db, fragment)
		if got, err := s.summaries(ctx, 2); err == nil || got != nil {
			t.Fatalf("summaries %s: %+v %v", fragment, got, err)
		}
		dbtest.FailNextStatement(t, s.db, fragment)
		if got, err := s.projectHistory(ctx, projectID, 2); err == nil || got != nil {
			t.Fatalf("history %s: %+v %v", fragment, got, err)
		}
		dbtest.FailNextStatement(t, s.db, fragment)
		if got, err := s.historical(ctx, reviews[3]); err == nil || got != nil {
			t.Fatalf("historical lens %s: %+v %v", fragment, got, err)
		}
	}
	dbtest.FailNextStatement(t, s.db, "FROM conformance_review_exceptions")
	if got, err := s.historical(ctx, reviews[3]); err == nil || got != nil {
		t.Fatalf("snapshot exceptions: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, s.db, "FROM conformance_adjudications a")
	if got, err := s.historical(ctx, reviews[3]); err == nil || got != nil {
		t.Fatalf("prior confirmations: %+v %v", got, err)
	}
	if got, err := s.detail(ctx, idstest.N(999999)); got != nil || !errors.Is(err, ErrNoReview) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("missing detail: %+v %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.detail(cancelled, reviews[0]); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %+v %v", got, err)
	}
	if got, err := s.detail(ctx, reviews[0]); err != nil || len(got.Verdicts) != 1 {
		t.Fatalf("read after failures: %+v %v", got, err)
	}
}

func TestReviewDetailUsesOneSnapshot(t *testing.T) {
	t.Parallel()
	s, projectID, _ := conformanceHistoryFixture(t)
	r, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "alice", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readConformance(context.Background(), s.db, func(db database.Queryer) (*ReviewDetail, error) {
		before, err := readReview(context.Background(), db, r.ID)
		if err != nil {
			return nil, err
		}
		// Commit a verdict and finish from another connection after the reader's
		// snapshot was established. Neither fact belongs to its earlier view.
		_, err = s.recordVerdict(context.Background(), verdictAdmission{Verdict: Verdict{ReviewID: r.ID, ProjectID: projectID, ProjectSlug: "history", SHA: "new", Block: "another", Version: 1, Check: "new", Evaluator: "sh", State: Conforms, At: stamp.Of(time.Now()).String()}})
		if err != nil {
			return nil, err
		}
		if _, err := s.finish(context.Background(), *r, ReviewDone); err != nil {
			return nil, err
		}
		vs, err := readReviewVerdicts(context.Background(), db, []ids.ID{r.ID}, ids.Nil)
		return &ReviewDetail{Review: before, Verdicts: vs}, err
	})
	if err != nil || got.Review.Status != ReviewRunning || len(got.Verdicts) != 0 {
		t.Fatalf("mixed snapshot: %+v %v", got, err)
	}
	after, err := s.detail(context.Background(), r.ID)
	if err != nil || after.Review.Status != ReviewDone || after.Review.Verdicts != 1 || len(after.Verdicts) != 1 {
		t.Fatalf("fresh snapshot missing settlement: %+v %v", after, err)
	}
}

func TestHistoricalPriorConfirmationCannotComeFromLaterVerdict(t *testing.T) {
	t.Parallel()
	s, _, reviews := conformanceHistoryFixture(t)
	vs, err := readReviewVerdicts(context.Background(), s.db, []ids.ID{reviews[3]}, ids.Nil)
	if err != nil || len(vs) != 1 {
		t.Fatalf("verdict: %+v %v", vs, err)
	}
	if _, err := s.confirm(context.Background(), confirmationRequest{VerdictID: vs[0].ID, By: "alice"}); err != nil {
		t.Fatal(err)
	}
	old, err := s.historical(context.Background(), reviews[0])
	if err != nil || len(old.PriorConfirmations) != 0 {
		t.Fatalf("future confirmation leaked into prior evidence: %+v %v", old, err)
	}
	newer, err := s.historical(context.Background(), reviews[4])
	if err != nil || len(newer.PriorConfirmations) != 1 {
		t.Fatalf("earlier confirmation missing: %+v %v", newer, err)
	}
	for _, p := range newer.PriorConfirmations {
		if p.SHA != vs[0].SHA || p.By != "alice" {
			t.Fatalf("wrong earlier evidence: %+v", p)
		}
	}
}

func TestFixQueryRejectsCorruptAttributionAndEmptyIsAList(t *testing.T) {
	t.Parallel()
	s, projectID, reviews := conformanceHistoryFixture(t)
	ctx := context.Background()
	if got, err := s.projectFixes(ctx, projectID, 10); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty fixes: %+v %v", got, err)
	}
	vs, err := readReviewVerdicts(ctx, s.db, reviews, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for i, attribution := range []string{`"not a list"`, `["00000000-002a-7000-8000-000000000000"]`} {
		if _, err := queries(s.db).InsertFix(ctx, conformancesql.InsertFixParams{ID: ids.New(stamp.Of(time.Now()).Time), ProjectID: projectID, CheckKey: "standard@1·clear", BeforeVerdictID: vs[0].ID, AfterVerdictID: vs[i+1].ID, ChangeIdsJson: []byte(attribution), CreatedAt: stamp.Of(time.Now()).Time}); err != nil {
			t.Fatal(err)
		}
	}
	// A change that is not on record is named by no number (0), index for index.
	if got, err := s.projectFixes(ctx, projectID, 1); err != nil || len(got) != 1 || len(got[0].ChangeIDs) != 1 || got[0].ChangeIDs[0] != idstest.N(42) || len(got[0].ChangeNumbers) != 1 || got[0].ChangeNumbers[0] != 0 || got[0].ReviewNumber <= 0 {
		t.Fatalf("bounded fixes: %+v %v", got, err)
	}
	if got, err := s.projectFixes(ctx, projectID, 10); err == nil || got != nil {
		t.Fatalf("corrupt row produced partial success: %+v %v", got, err)
	}
}

// A query for no recorded verdicts returns a JSON list, including an unknown review.
func TestReviewVerdictsEmptyIsAList(t *testing.T) {
	t.Parallel()
	out, err := readReviewVerdicts(context.Background(), testDB(t), []ids.ID{idstest.N(999999)}, ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("want an empty non-nil list, got %#v", out)
	}
}
