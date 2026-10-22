package conformance

import (
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/stamp"

	"github.com/flokkit/apptown/internal/database"
)

// Historical lenses use the saved verdicts and exception snapshots. Current
// adoption and live exception state never rewrite a past review's evidence.
type historicalConformance struct {
	Projects  []Project
	CheckDefs map[string]CheckDefinition
	Reviews   []Review
	ReviewDetail
	Exceptions         []Disposition
	Adjudications      map[ids.ID]Adjudication
	PriorConfirmations map[ids.ID]PriorConfirmation
	Delta              *ReviewDelta
}

func readPriorConfirmations(ctx context.Context, db database.Queryer, vs []Observation, adj map[ids.ID]Adjudication) (map[ids.ID]PriorConfirmation, error) {
	idList := []ids.ID{}
	for _, v := range vs {
		if v.Record != nil && !v.Record.ID.IsZero() && LaneOf(v, adj) == LaneFlagged {
			idList = append(idList, v.Record.ID)
		}
	}
	out := map[ids.ID]PriorConfirmation{}
	if len(idList) == 0 {
		return out, nil
	}
	type prior struct {
		id ids.ID
		PriorConfirmation
	}
	found, err := queries(db).PriorAdjudicatedVerdicts(ctx, idList)
	if err != nil {
		return nil, err
	}
	rows := make([]prior, 0, len(found))
	for _, r := range found {
		rows = append(rows, prior{id: r.ID, PriorConfirmation: PriorConfirmation{SHA: r.Sha, By: r.By, At: stamp.Of(r.CreatedAt).String()}})
	}
	for _, p := range rows {
		out[p.id] = p.PriorConfirmation
	}
	return out, nil
}

func (s reviewStore) historical(ctx context.Context, id ids.ID) (*historicalConformance, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) (*historicalConformance, error) {
		r, err := readReview(ctx, db, id)
		if err != nil {
			return nil, err
		}
		h := &historicalConformance{ReviewDetail: ReviewDetail{Review: r}}
		idList := []ids.ID{id}
		var prev ids.ID
		if r.Status == ReviewDone {
			earlier, err := queries(db).PriorReviewBefore(ctx, conformancesql.PriorReviewBeforeParams{ID: id, Status: ReviewDone.String()})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			prev = earlier.ID
			if !prev.IsZero() {
				idList = append(idList, prev)
			}
		}
		vs, err := readReviewVerdicts(ctx, db, idList, ids.Nil)
		if err != nil {
			return nil, err
		}
		byReview := verdictsByReview(vs)
		h.Verdicts = byReview[id]
		if h.Verdicts == nil {
			h.Verdicts = []Verdict{}
		}
		if !prev.IsZero() {
			h.Delta = CompareReviews(prev, h.Verdicts, byReview[prev])
		}
		snapshots, err := queries(db).ReviewExceptionsForReview(ctx, id)
		if err != nil {
			return nil, err
		}
		h.Exceptions = make([]Disposition, 0, len(snapshots))
		for _, r := range snapshots {
			d, err := reviewException(r)
			if err != nil {
				return nil, err
			}
			h.Exceptions = append(h.Exceptions, d)
		}
		h.Adjudications, err = readAdjudications(ctx, db, verdictIDs(h.Verdicts))
		if err != nil {
			return nil, err
		}
		h.PriorConfirmations, err = readPriorConfirmations(ctx, db, RecordedObservations(h.Verdicts), h.Adjudications)
		if err != nil {
			return nil, err
		}
		projectIDs := []ids.ID{}
		seen := map[ids.ID]bool{}
		for _, v := range h.Verdicts {
			if !seen[v.ProjectID] {
				seen[v.ProjectID] = true
				projectIDs = append(projectIDs, v.ProjectID)
			}
		}
		h.Projects, err = readConformanceProjects(ctx, db, projectIDs, false)
		if err != nil {
			return nil, err
		}
		h.CheckDefs, err = readHistoricalCheckDefs(ctx, db, s.checks, h.Verdicts)
		if err != nil {
			return nil, err
		}
		h.Reviews, err = readConformanceReviews(ctx, db)
		if err != nil {
			return nil, err
		}
		return h, nil
	})
}
