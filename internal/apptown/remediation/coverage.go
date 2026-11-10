package remediation

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// coverage is work already under way on one adopted check: an open
// remediation change, or a queued item of a job that has not made its change
// yet. A queued item has no change number; its job's number is what a
// person follows.
type coverage struct {
	ProjectID         ids.ID
	ChangeID          ids.ID
	ChangeNumber      int64
	RemediationID     ids.ID
	RemediationNumber int64
	Block             string
	Version           int
	Check             string
}

// refusal is why a second remediation of a covered finding is not started.
func (c coverage) refusal() error {
	if c.ChangeID.IsZero() {
		return fault.New(fault.Conflict, fmt.Sprintf("This finding is already queued in remediation #%d. Open it to follow its progress.", c.RemediationNumber))
	}
	return fault.New(fault.Conflict, fmt.Sprintf("This finding is being fixed in change #%d. Open that change to follow its progress.", c.ChangeNumber))
}

// CoverageIn reads what covers findings in the caller's authorized projects:
// workflow's open remediation changes and this owner's queued items. Only
// the "is it being worked on" readers use it (the fleet view and starting a
// remediation); a change's own admission keeps workflow's open-change check,
// so a queued item is never refused by its own reservation.
func CoverageIn(ctx context.Context, q database.Queryer, projects []ids.ID) ([]coverage, error) {
	open, err := workflow.OpenRemediationsIn(ctx, q, projects)
	if err != nil {
		return nil, err
	}
	queued, err := queries(q).QueuedItemCoverage(ctx, projects)
	if err != nil {
		return nil, err
	}
	out := make([]coverage, 0, len(open)+len(queued))
	for _, c := range open {
		out = append(out, coverage{ProjectID: c.ProjectID, ChangeID: c.ChangeID, ChangeNumber: c.Number, Block: c.Block, Version: c.Version, Check: c.Check})
	}
	for _, r := range queued {
		out = append(out, coverage{ProjectID: r.ProjectID, RemediationID: r.RemediationID, RemediationNumber: r.Number, Block: r.Block, Version: int(r.Version), Check: r.CheckName})
	}
	return out, nil
}
