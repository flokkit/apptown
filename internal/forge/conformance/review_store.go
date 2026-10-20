package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/stamp"
)

// reviewStore owns review progress, terminal outcomes, verdict/fix admission,
// confirmations and exception snapshots independently of evaluation. Required
// receipts commit with their operation; storage failures and stale prerequisites
// return errors instead of detached success records.
type reviewStore struct {
	db     *database.DB
	checks CheckSource
	clock  func() time.Time // AppTown's clock; nil is the real one
}

func (s reviewStore) now() time.Time { return clock.Or(s.clock)() }

func (s reviewStore) create(ctx context.Context, q database.Queryer, in ReviewRequest, actor string, status ReviewState) (*Review, error) {
	// The number lock holds until the transaction ends: outside a caller's
	// transaction the insert opens its own.
	var db *database.Tx
	switch q := q.(type) {
	case *database.Tx:
		db = q
	case *database.DB:
		return database.Transact(ctx, q, func(tx *database.Tx) (*Review, error) {
			return s.create(ctx, tx, in, actor, status)
		})
	default:
		return nil, fmt.Errorf("a review is created in the database or a transaction, not %T", q)
	}
	trigger := in.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	// JSON keeps the difference between the whole estate (null) and no
	// projects ([]).
	selected, err := json.Marshal(in.Projects)
	if err != nil {
		return nil, err
	}
	number, err := queries(db).NextReviewNumber(ctx)
	if err != nil {
		return nil, err
	}
	starter, err := identity.ActorIn(ctx, db, actor)
	if err != nil {
		return nil, err
	}
	row, err := queries(db).InsertReview(ctx, conformancesql.InsertReviewParams{ID: ids.New(stamp.Of(s.now()).Time), Number: number,
		Trigger: trigger, Actor: actor, ActorPrincipal: starter.Principal, ActorVia: starter.Via, StartedAt: stamp.Of(s.now()).Time, Status: status.String(),
		SelectedProjects: string(selected), Build: in.Build, BuildBudget: int64(in.BuildBudget),
	})
	if err != nil {
		return nil, err
	}
	r, err := review(row)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// reviewRequest is what a saved review was asked to do.
func reviewRequest(row conformancesql.ConformanceReview) (ReviewRequest, error) {
	in := ReviewRequest{Trigger: row.Trigger, Build: row.Build, BuildBudget: int(row.BuildBudget)}
	if err := json.Unmarshal(row.SelectedProjects, &in.Projects); err != nil {
		return ReviewRequest{}, fmt.Errorf("review %d's selection: %w", row.ID, err)
	}
	return in, nil
}

// Progress is allowed only while running; late work cannot rewrite a terminal
// review. A zero-row update is a conflict, never a successful receipt.
func (s reviewStore) progress(ctx context.Context, r Review) error {
	n, err := queries(s.db).UpdateReviewProgress(ctx, conformancesql.UpdateReviewProgressParams{
		ID: r.ID, ProjectsTotal: int64(r.ProjectsTotal), ProjectsDone: int64(r.ProjectsDone),
		CurrentProject: r.CurrentProject, Status: ReviewRunning.String(),
	})
	return reviewRowsWritten(n, err)
}

// A zero-row write means the review moved on: a conflict, never a receipt.
func reviewRowsWritten(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrReviewClosed
	}
	return nil
}

// Return a terminal snapshot only after its write succeeds. Callers retain the
// running snapshot when the database cannot confirm the terminal state.
func (s reviewStore) finish(ctx context.Context, r Review, status ReviewState) (*Review, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Review, error) {
		current, err := lockReview(ctx, tx, r.ID)
		if err != nil {
			return nil, err
		}
		next, err := current.Transition(status)
		if err != nil {
			return nil, err
		}
		counted, err := queries(tx).VerdictCountForReview(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		r.Verdicts = int(counted)
		r.FinishedAt, r.Status, r.CurrentProject = stamp.Of(s.now()), next, ""
		n, err := queries(tx).FinishReview(ctx, conformancesql.FinishReviewParams{
			ID: r.ID, FinishedAt: r.FinishedAt.NullTime, Projects: int64(r.Projects), Verdicts: int64(r.Verdicts),
			Errors: int64(r.Errors), Cost: r.Cost, Status: r.Status.String(), ProjectsDone: int64(r.ProjectsDone),
			Status_2: current.String(),
		})
		if err := reviewRowsWritten(n, err); err != nil {
			return nil, err
		}
		return &r, nil
	})
}

func (s reviewStore) latestVerdict(ctx context.Context, projectID ids.ID, block string, version int, check string) (Verdict, error) {
	return readLatestVerdict(ctx, s.db, projectID, block, version, check)
}

func readLatestVerdict(ctx context.Context, db database.Queryer, projectID ids.ID, block string, version int, check string) (Verdict, error) {
	row, err := queries(db).LatestVerdictForCheck(ctx, conformancesql.LatestVerdictForCheckParams{
		ProjectID: projectID, Block: block, Version: int64(version), CheckName: check,
	})
	if err != nil {
		return Verdict{}, err
	}
	return verdict(row)
}

func lockReview(ctx context.Context, tx *database.Tx, id ids.ID) (ReviewState, error) {
	row, err := queries(tx).ReviewByIDForUpdate(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrReviewClosed
	}
	if err != nil {
		return 0, err
	}
	return ParseReviewState(row.Status)
}

func lockRunningReview(ctx context.Context, tx *database.Tx, id ids.ID) error {
	status, err := lockReview(ctx, tx, id)
	if err != nil {
		return err
	}
	return status.RequireRunning()
}

// Startup participates in the caller's larger repair transaction. It uses the
// same transition rule as normal settlement; there is no independent commit or
// resumed evaluation. The explicit state predicate is the bulk admission guard.
// RepairInterruptedReviews participates in the startup repair transaction.
// Invoke before admitting requests or workers; the caller owns commit. at
// is AppTown's clock, which stamps the reviews' ends. A queued review
// ends with the running ones: nothing starts after a restart, and the job
// that later delivers it finds it ended.
func RepairInterruptedReviews(ctx context.Context, tx *database.Tx, at time.Time) (int64, error) {
	for _, from := range []ReviewState{ReviewQueued, ReviewRunning} {
		if _, err := from.Transition(ReviewError); err != nil {
			return 0, err
		}
	}
	return queries(tx).AbandonUnfinishedReviews(ctx, conformancesql.AbandonUnfinishedReviewsParams{Status: ReviewError.String(), Queued: ReviewQueued.String(), Running: ReviewRunning.String(), At: stamp.Of(at).Time})
}

func (s reviewStore) recordVerdict(ctx context.Context, req verdictAdmission) (Verdict, error) {
	v := req.Verdict
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Verdict, error) {
		if err := lockConformanceProject(ctx, tx, v.ProjectID); err != nil {
			return Verdict{}, err
		}
		if err := lockRunningReview(ctx, tx, v.ReviewID); err != nil {
			return Verdict{}, err
		}
		prev, err := readLatestVerdict(ctx, tx, v.ProjectID, v.Block, v.Version, v.Check)
		if _, err := database.Lookup(err); err != nil {
			return Verdict{}, err
		}
		if prev.ID != req.PreviousID {
			return Verdict{}, ErrVerdictChanged
		}
		needed, err := fixRequired(ctx, tx, prev, v)
		if err != nil {
			return Verdict{}, err
		}
		if needed != (req.Fix != nil) {
			return Verdict{}, ErrVerdictChanged
		}
		at := stamp.Parse(v.At)
		row, err := queries(tx).InsertVerdict(ctx, conformancesql.InsertVerdictParams{ID: ids.New(at.Time),
			ReviewID: v.ReviewID, ProjectID: v.ProjectID, ProjectSlug: v.ProjectSlug, Sha: v.SHA,
			Block: v.Block, Version: int64(v.Version), CheckName: v.Check, Evaluator: v.Evaluator,
			EvaluatorVer: v.EvaluatorVer, Mode: v.Mode, State: v.State.String(), Severity: v.Severity,
			Detail: v.Detail, CreatedAt: at.Time, ExceptedBy: v.ExceptedBy, EvidenceHash: v.EvidenceHash,
		})
		if err != nil {
			return Verdict{}, err
		}
		v.ID = row.ID
		if err := recordFixIn(ctx, tx, req, v, s.now()); err != nil {
			return Verdict{}, err
		}
		return v, nil
	})
}

// Snapshot all active exceptions or none. Required read/write errors abort the
// review instead of producing an incomplete historical lens.
func (s reviewStore) snapshotExceptions(ctx context.Context, reviewID, projectID ids.ID) ([]Disposition, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) ([]Disposition, error) {
		if err := lockRunningReview(ctx, tx, reviewID); err != nil {
			return nil, err
		}
		ds, err := readDispositions(ctx, tx, []ids.ID{projectID}, true, s.now())
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			created := stamp.Parse(d.CreatedAt)
			if _, err := queries(tx).InsertReviewException(ctx, conformancesql.InsertReviewExceptionParams{ID: ids.New(created.Time),
				ReviewID: reviewID, ProjectID: projectID, DispositionID: d.ID, Fingerprint: d.Fingerprint,
				Identifier: d.Identifier, Package: d.Package, Status: d.Status.String(),
				Justification: d.Justification, By: d.By, ByPrincipal: d.ByPrincipal, ByVia: d.byVia, CreatedAt: created.Time, ExpiresAt: d.ExpiresAt.NullTime,
			}); err != nil {
				return nil, err
			}
		}
		return ds, nil
	})
}

func readConformanceReviews(ctx context.Context, db database.Queryer) ([]Review, error) {
	rows, err := queries(db).AllReviews(ctx)
	if err != nil {
		return nil, err
	}
	return reviews(rows)
}
