package workflow

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// SupersededChange is an already-admitted abandoned Change's explanation. The
// caller composes it with abandonment and the replacement remediation attempt.
type SupersededChange struct {
	Change ChangeRef
	Reason string
}

func RecordSupersededChangeIn(ctx context.Context, tx *database.Tx, in SupersededChange) error {
	if in.Change.ID.IsZero() || in.Change.ProjectID.IsZero() || strings.TrimSpace(in.Reason) == "" {
		return errChangeEditConflict
	}
	n, err := workflowsql.New(tx.SQL()).RecordSupersededChange(ctx, workflowsql.RecordSupersededChangeParams{Summary: in.Reason, ID: in.Change.ID, ProjectID: in.Change.ProjectID, State: ChangeAbandoned.String()})
	if err != nil {
		return err
	}
	if n != 1 {
		return errChangeEditConflict
	}
	return nil
}

// UpliftAdoptionIn locks and checks the Change before the adoption owner changes
// a project's revision, including on replay. The caller already holds the project
// lock and retains all adoption revision/provenance checks in the same transaction.
func UpliftAdoptionIn(ctx context.Context, tx *database.Tx, ref ChangeRef) (int64, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return 0, errChangeEditConflict
	}
	ch, err := changeRecord(workflowsql.New(tx.SQL()).ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: ref.ID}))
	if err != nil {
		return 0, err
	}
	if ch.ProjectID != ref.ProjectID || ch.Type != "uplift" || ch.State != ChangeShipped {
		return 0, fmt.Errorf("%w: adoption requires this project's shipped uplift Change", errChangeEditConflict)
	}
	return ch.AdoptedRevision, nil
}

type UpliftAdoption struct {
	Change   ChangeRef
	Revision int64
}

// StampUpliftAdoptionIn records the adoption owner's new revision. The owner
// supplies its durable adoption receipt in this transaction; Work owns the stamp.
func StampUpliftAdoptionIn(ctx context.Context, tx *database.Tx, in UpliftAdoption) error {
	if in.Change.ID.IsZero() || in.Change.ProjectID.IsZero() || in.Revision <= 0 {
		return errChangeEditConflict
	}
	n, err := workflowsql.New(tx.SQL()).StampUpliftAdoption(ctx, workflowsql.StampUpliftAdoptionParams{AdoptedRevision: in.Revision, ID: in.Change.ID, ProjectID: in.Change.ProjectID, State: ChangeShipped.String()})
	if err != nil {
		return err
	}
	if n != 1 {
		return errChangeEditConflict
	}
	return nil
}
