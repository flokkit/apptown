package remediation

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/forge/workflow"
)

// Remediation owns this budget; its guarded receipt participates in Work's batch.
// LandingRetryBudget charges a landing's transient retries; Now is the
// forge's clock (nil the real one).
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
