package remediation

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Remediation owns this budget; its guarded receipt participates in Work's batch.
// LandingRetryBudget charges a landing's transient retries; Now is the
// AppTown's clock (nil the real one).
type LandingRetryBudget struct{ Now func() time.Time }

func (b LandingRetryBudget) ChargeIn(ctx context.Context, tx *database.Tx, projectID, changeID ids.ID, retry workflow.LandingRetry) error {
	n, err := queries(tx).ChargeTransientRetry(ctx, remediationsql.ChargeTransientRetryParams{UpdatedAt: clock.Or(b.Now)(),
		ID: retry.ItemID, Reason: retry.Reason, ProjectID: projectID, ChangeID: changeID,
		Attempt: int64(retry.Attempt), TransientRetries: int64(retry.Retries), TransientRetries_2: int64(remMaxTransients),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("the remediation item changed or exhausted its landing retries")
	}
	return nil
}
