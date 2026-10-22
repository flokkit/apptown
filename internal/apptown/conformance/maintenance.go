package conformance

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

var ErrHistoryBusy = fault.New(fault.Conflict, "history is in use; finish active work and retry clearing it")

// HistoryRemoval contains provisional counts inside the caller's transaction.
// The cross-domain coordinator must append its required receipt and commit.
// Project exception decisions are not review history and are preserved.
type HistoryRemoval struct {
	Reviews       int64  `json:"conformanceReviews"`
	Verdicts      int64  `json:"conformanceVerdicts"`
	Adjudications int64  `json:"conformanceAdjudications"`
	Exceptions    int64  `json:"conformanceReviewExceptions"`
	Fixes         int64  `json:"conformanceFixes"`
	By            string `json:"-"`
}

// PrepareHistoryClearIn admits the conformance part of history maintenance.
// NOWAIT refuses concurrent readers/writers instead of introducing a
// maintenance queue or a new admission protocol. Locks remain until commit.
func (s *Service) PrepareHistoryClearIn(ctx context.Context, tx *database.Tx) (*HistoryClear, error) {
	who, err := s.require(ctx, Permission{Action: ClearHistory})
	if err != nil {
		return nil, err
	}
	if err := queries(tx).LockConformanceHistory(ctx); err != nil {
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "55P03" {
			return nil, ErrHistoryBusy
		}
		return nil, err
	}
	// A queued review is admitted work waiting for its worker: clearing
	// would delete the request its job reads.
	unfinished, err := queries(tx).UnfinishedReviewExists(ctx, conformancesql.UnfinishedReviewExistsParams{Queued: ReviewQueued.String(), Running: ReviewRunning.String()})
	if err != nil {
		return nil, err
	}
	if unfinished {
		return nil, ErrHistoryBusy
	}
	held, err := queries(tx).ReviewRecordCounts(ctx)
	if err != nil {
		return nil, err
	}
	counts := HistoryRemoval{By: who, Reviews: held.Reviews, Verdicts: held.Verdicts,
		Adjudications: held.Adjudications, Exceptions: held.Exceptions, Fixes: held.Fixes}
	return &HistoryClear{tx: tx, counts: counts}, nil
}

// HistoryClear holds the locked, authorized counts in the caller's transaction.
// Other owners may check admission before any history is removed.
type HistoryClear struct {
	tx     *database.Tx
	counts HistoryRemoval
}

func (h *HistoryClear) Remove(ctx context.Context) (*HistoryRemoval, error) {
	if h == nil || h.tx == nil {
		return nil, ErrScope
	}
	tx := h.tx
	counts := h.counts
	// Children first: counts describe direct removal, not implicit cascades.
	q := queries(tx)
	for _, table := range []struct {
		name     string
		remove   func(context.Context) (int64, error)
		expected int64
	}{
		{"conformance_fixes", q.DeleteAllFixes, counts.Fixes},
		{"conformance_review_exceptions", q.DeleteAllReviewExceptions, counts.Exceptions},
		{"conformance_adjudications", q.DeleteAllAdjudications, counts.Adjudications},
		{"conformance_verdicts", q.DeleteAllVerdicts, counts.Verdicts},
		{"conformance_reviews", q.DeleteAllReviews, counts.Reviews},
	} {
		n, err := table.remove(ctx)
		if err != nil {
			return nil, err
		}
		if n != table.expected {
			return nil, fmt.Errorf("clearing %s removed %d of %d records", table.name, n, table.expected)
		}
	}
	return &counts, nil
}
