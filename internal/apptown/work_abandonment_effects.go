package apptown

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

const workflowAbandonmentJob = "workflow-abandonment"

// abandonmentEffects records what a Change's abandonment ends, at the
// AppTown's clock (nil is the real one).
type abandonmentEffects struct{ now func() time.Time }

// External ownership records join the Work transaction; no container or Git work
// runs here. Existing jobs perform cleanup after commit with their usual limits.
func (e abandonmentEffects) RecordAbandonIn(ctx context.Context, tx *database.Tx, in workflow.AbandonEffects) error {
	if in.CloseChange {
		if err := identity.RevokeChangeTicketsIn(ctx, tx, in.ChangeID, clock.Or(e.now)()); err != nil {
			return err
		}
		return recordAbandonJob(ctx, tx, fmt.Sprintf("change/%d/cleanup", in.ChangeID), map[string]any{"changeId": in.ChangeID, "runIds": in.RunIDs})
	}
	if in.Root && !in.ChangeID.IsZero() {
		return recordAbandonJob(ctx, tx, fmt.Sprintf("run/%d/narrative", in.RunID), map[string]any{"runId": in.RunID, "changeId": in.ChangeID})
	}
	return nil
}
func recordAbandonJob(ctx context.Context, tx *database.Tx, key string, values map[string]any) error {
	return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: workflowAbandonmentJob, Key: key, Values: values})
}
