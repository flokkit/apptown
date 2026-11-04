package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type heldSummaryRequests interface {
	RequestHeldSummaryIn(context.Context, *database.Tx, ids.ID) error
}

// requestHeldSummaryIn asks the existing narrative queue when a check or approval first holds.
// Rework and running checks take precedence through the normal classifier.
func requestHeldSummaryIn(ctx context.Context, tx *database.Tx, id ids.ID, requests heldSummaryRequests, before *summaryHold) error {
	if requests == nil {
		return nil
	}
	observed, err := readRun(ctx, tx, id)
	if err != nil {
		return err
	}
	run, err := lockRunSnapshotIn(ctx, tx, observed)
	if err != nil {
		return err
	}
	if run.ChangeID.IsZero() {
		return nil
	}
	d, err := readWorkflowSnapshot(ctx, tx, id, DetailView, nil, run.ProjectID, nil)
	if err != nil {
		return err
	}
	hold := summaryHolding(d)
	if before != nil && hold == *before {
		return nil
	}
	if hold.kind != "check" && hold.kind != "approval" || hold.run != run.ID {
		return nil
	}
	return requests.RequestHeldSummaryIn(ctx, tx, run.ChangeID)
}

// Compare the held target and recorded attempt, rather than summary job state:
// a completed job must not make another observation of the hold request it again.
type summaryHold struct {
	kind      string
	run, step ids.ID
	attempt   int64
	status    StepState
}

func summaryHolding(d *RunSnapshot) summaryHold {
	w := waitingOn(nil, d)
	h := summaryHold{kind: w.Kind, run: w.Run, step: w.Step}
	if step := d.activity.holdingStep; step != nil {
		h.attempt, h.status = step.attempt, step.state
	}
	return h
}
