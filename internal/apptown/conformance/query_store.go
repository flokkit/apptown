package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/database"
)

// Related historical facts come from one database snapshot. Query results never
// escape after a failed read/commit, and callers cannot mutate the saved records.
func readConformance[T any](ctx context.Context, db *database.DB, read func(database.Queryer) (T, error)) (T, error) {
	tx, err := db.ReadSnapshot(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := read(tx)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

type ReviewDetail struct {
	Review   *Review       `json:"review"`
	Verdicts []Observation `json:"verdicts"`
}

// ErrNoReview and ErrNoVerdict are the absence of the review or verdict a
// caller named by id, classified here so callers never read sql.ErrNoRows.
var (
	errNoReview  = fault.New(fault.NotFound, "no conformance review has id")
	errNoVerdict = fault.New(fault.NotFound, "no conformance verdict has id")
)

// readReview reads the review a caller named; absence is ErrNoReview.
func readReview(ctx context.Context, db database.Queryer, id ids.ID) (*Review, error) {
	row, err := queries(db).ReviewByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %d", errNoReview, id)
	}
	if err != nil {
		return nil, err
	}
	r, err := review(row)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// projectID zero is the instance review projection; a project query always
// constrains the rows in SQL, before decoding or computing standing.
func readReviewVerdicts(ctx context.Context, db database.Queryer, idList []ids.ID, projectID ids.ID) ([]Verdict, error) {
	if len(idList) == 0 {
		return []Verdict{}, nil
	}
	rows, err := queries(db).VerdictsForReviews(ctx, conformancesql.VerdictsForReviewsParams{Column1: idList, Column2: projectID})
	if err != nil {
		return nil, err
	}
	return verdicts(rows)
}

func (s reviewStore) detail(ctx context.Context, id ids.ID) (*ReviewDetail, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) (*ReviewDetail, error) {
		r, err := readReview(ctx, db, id)
		if err != nil {
			return nil, err
		}
		vs, err := readReviewVerdicts(ctx, db, []ids.ID{id}, ids.Nil)
		if err != nil {
			return nil, err
		}
		adj, err := readAdjudications(ctx, db, verdictIDs(vs))
		if err != nil {
			return nil, err
		}
		return &ReviewDetail{Review: r, Verdicts: RecordedObservations(vs, adj)}, nil
	})
}

func verdictIDs(vs []Verdict) []ids.ID {
	idList := make([]ids.ID, 0, len(vs))
	for _, v := range vs {
		if !v.ID.IsZero() {
			idList = append(idList, v.ID)
		}
	}
	return idList
}

func verdictsByReview(vs []Verdict) map[ids.ID][]Verdict {
	out := map[ids.ID][]Verdict{}
	for _, v := range vs {
		out[v.ReviewID] = append(out[v.ReviewID], v)
	}
	return out
}

// Select the page and its nearest earlier completed review together. The extra
// record preserves the final row's comparison when its predecessor is off-page.
func readReviewSummaries(ctx context.Context, db database.Queryer, limit int) ([]ReviewSummary, error) {
	bound := int64(0)
	if limit > 0 {
		bound = int64(limit)
	}
	rows, err := queries(db).ReviewSummariesPage(ctx, conformancesql.ReviewSummariesPageParams{
		Column1: bound, Status: ReviewDone.String(),
	})
	if err != nil {
		return nil, err
	}
	reviews, err := reviews(rows)
	if err != nil {
		return nil, err
	}
	idList := make([]ids.ID, 0, len(reviews))
	previous := map[ids.ID]ids.ID{}
	var prior ids.ID
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		idList = append(idList, r.ID)
		previous[r.ID] = prior
		if r.Status == ReviewDone {
			prior = r.ID
		}
	}
	vs, err := readReviewVerdicts(ctx, db, idList, ids.Nil)
	if err != nil {
		return nil, err
	}
	adj, err := readAdjudications(ctx, db, verdictIDs(vs))
	if err != nil {
		return nil, err
	}
	byReview := verdictsByReview(vs)
	if limit > 0 && len(reviews) > limit {
		reviews = reviews[:limit]
	}
	out := make([]ReviewSummary, 0, len(reviews))
	for _, r := range reviews {
		s := ReviewSummary{Review: r, StatusLabel: r.Status.display()}
		for _, pvs := range groupByProject(RecordedObservations(byReview[r.ID], adj)) {
			ps := SummarizeProject(ids.Nil, "", pvs)
			s.Findings += ps.Lanes.Findings
			s.Flagged += ps.Lanes.Flagged
			s.Judged += ps.Judged
			s.Total += ps.Total
			if ps.Lanes.Findings > 0 {
				s.ProjectsWithFindings++
			}
		}
		if prev := previous[r.ID]; r.Status == ReviewDone && !prev.IsZero() {
			s.Delta = compareReviews(prev, byReview[r.ID], byReview[prev])
			s.Delta.FixedItems, s.Delta.NewItems = nil, nil
		}
		out = append(out, s)
	}
	return out, nil
}

func (s reviewStore) summaries(ctx context.Context, limit int) ([]ReviewSummary, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) ([]ReviewSummary, error) {
		return readReviewSummaries(ctx, db, limit)
	})
}

func readProjectHistory(ctx context.Context, db database.Queryer, projectID ids.ID, limit int) ([]HistoryPoint, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := queries(db).ReviewsTouchingProject(ctx, conformancesql.ReviewsTouchingProjectParams{
		ProjectID: projectID, Limit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	reviews, err := reviews(rows)
	if err != nil {
		return nil, err
	}
	idList := make([]ids.ID, 0, len(reviews))
	for _, r := range reviews {
		idList = append(idList, r.ID)
	}
	vs, err := readReviewVerdicts(ctx, db, idList, projectID)
	if err != nil {
		return nil, err
	}
	adj, err := readAdjudications(ctx, db, verdictIDs(vs))
	if err != nil {
		return nil, err
	}
	byReview := verdictsByReview(vs)
	out := make([]HistoryPoint, 0, len(reviews))
	for _, r := range reviews {
		ps := SummarizeProject(projectID, "", RecordedObservations(byReview[r.ID], adj))
		at := r.StartedAt
		if r.FinishedAt.Valid {
			at = r.FinishedAt.String()
		}
		out = append(out, HistoryPoint{ReviewID: r.ID, At: at, Findings: ps.Lanes.Findings, Flagged: ps.Lanes.Flagged,
			Excepted: ps.Lanes.Excepted, Unassessed: ps.Lanes.Unassessed, Broken: ps.Lanes.Broken, Total: ps.Total, Standing: ps.Standing})
	}
	return out, nil
}

func (s reviewStore) projectHistory(ctx context.Context, id ids.ID, limit int) ([]HistoryPoint, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) ([]HistoryPoint, error) {
		return readProjectHistory(ctx, db, id, limit)
	})
}

type recordedStanding struct {
	ProjectStanding
	ReviewID ids.ID
}

func (s reviewStore) latestStanding(ctx context.Context, projectID ids.ID, slug string) (*recordedStanding, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) (*recordedStanding, error) {
		rows, err := queries(db).LatestVerdictsForProject(ctx, projectID)
		if err != nil {
			return nil, err
		}
		vs, err := verdicts(rows)
		if err != nil || len(vs) == 0 {
			return nil, err
		}
		adj, err := readAdjudications(ctx, db, verdictIDs(vs))
		if err != nil {
			return nil, err
		}
		return &recordedStanding{ProjectStanding: SummarizeProject(projectID, slug, RecordedObservations(vs, adj)), ReviewID: vs[0].ReviewID}, nil
	})
}

func (s reviewStore) projectFixes(ctx context.Context, projectID ids.ID, limit int) ([]Fix, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := queries(s.db).FixesForProject(ctx, conformancesql.FixesForProjectParams{ProjectID: projectID, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Fix, 0, len(rows))
	for _, r := range rows {
		f, err := fix(r)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	byChange := map[ids.ID][]string{}
	for _, f := range out {
		if len(f.ChangeIDs) == 1 {
			byChange[f.ChangeIDs[0]] = append(byChange[f.ChangeIDs[0]], f.Check)
		}
	}
	for i := range out {
		if len(out[i].ChangeIDs) == 1 {
			for _, c := range byChange[out[i].ChangeIDs[0]] {
				if c != out[i].Check {
					out[i].Also = append(out[i].Also, c)
				}
			}
		}
	}
	return out, nil
}
