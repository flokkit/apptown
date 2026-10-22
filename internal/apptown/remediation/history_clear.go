package remediation

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type ClearedHistory struct {
	conformance.HistoryRemoval
	Attempts int64 `json:"remediationAttempts"`
	Items    int64 `json:"remediationItems"`
	Jobs     int64 `json:"remediations"`
}

// Clear refuses contention promptly. It never waits for a long-running job to
// stop, and its own database work is bounded. Both domains' history and the
// required receipt commit together; failed admission/removal returns no counts.
func (s *Service) ClearHistory(ctx context.Context) (*ClearedHistory, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*ClearedHistory, error) {
		admitted, err := s.deps.Checks.PrepareHistoryClearIn(ctx, tx)
		if err != nil {
			return nil, err
		}
		fixes, err := PrepareHistoryClearIn(ctx, tx)
		if err != nil {
			return nil, err
		}
		own, err := admitted.Remove(ctx)
		if err != nil {
			return nil, err
		}
		removed, err := fixes.Remove(ctx)
		if err != nil {
			return nil, err
		}
		counts := &ClearedHistory{HistoryRemoval: *own, Attempts: removed.Attempts, Items: removed.Items, Jobs: removed.Jobs}
		if err := audit.At(clock.Or(s.deps.Now)()).RecordIn(ctx, tx, counts.By, ids.Nil, "conformance.history-cleared", "fresh start", struct {
			Removed *ClearedHistory `json:"removed"`
		}{counts}); err != nil {
			return nil, err
		}
		return counts, nil
	})
}
