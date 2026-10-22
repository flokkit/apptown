package remediation

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/database"
)

// HistoryRemoval is provisional until the coordinator records its receipt and
// commits. A worker's pool settings are not history and remain untouched.
type HistoryRemoval struct {
	Attempts int64 `json:"remediation_attempts"`
	Items    int64 `json:"remediation_items"`
	Jobs     int64 `json:"remediations"`
}

// HistoryClear holds the admitted transaction. The coordinator sets its deadline
// and locks Conformance first; NOWAIT keeps this operation from waiting on work.
type HistoryClear struct{ tx *database.Tx }

func PrepareHistoryClearIn(ctx context.Context, tx *database.Tx) (*HistoryClear, error) {
	if err := queries(tx).LockRemediationHistory(ctx); err != nil {
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "55P03" {
			return nil, conformance.ErrHistoryBusy
		}
		return nil, err
	}
	active, err := queries(tx).ActiveRemediationExists(ctx, remediationsql.ActiveRemediationExistsParams{
		Status: RemRunning, Status_2: RemPaused,
	})
	if err != nil {
		return nil, err
	}
	if active {
		return nil, conformance.ErrHistoryBusy
	}

	return &HistoryClear{tx: tx}, nil
}
func (h *HistoryClear) Remove(ctx context.Context) (*HistoryRemoval, error) {
	tx := h.tx
	q := queries(tx)
	held, err := q.RemediationRecordCounts(ctx)
	if err != nil {
		return nil, err
	}
	counts := &HistoryRemoval{Attempts: held.Attempts, Items: held.Items, Jobs: held.Remediations}
	for _, table := range []struct {
		name     string
		remove   func(context.Context) (int64, error)
		expected int64
	}{
		{"remediation_attempts", q.DeleteAllAttempts, counts.Attempts},
		{"remediation_items", q.DeleteAllItems, counts.Items},
		{"remediations", q.DeleteAllRemediations, counts.Jobs},
	} {
		n, err := table.remove(ctx)
		if err != nil {
			return nil, err
		}
		if n != table.expected {
			return nil, fmt.Errorf("clearing %s removed %d of %d records", table.name, n, table.expected)
		}
	}

	return counts, nil
}
