package evals

import (
	"context"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/evals/internal/evalssql"
)

// Arm names the setup a copy runs.
const (
	ArmBaseline = "baseline"
	ArmVariant  = "variant"
)

// Copy is what the copy record says about a project: the eval and arm it
// was made for. Whether a project is a copy at all is Project's purpose
// (project.EvalCopy), which every effect owner asks; the copy record is
// Evals' membership, which outlives the copy and the eval so spend and
// complaints stay attributed.
type Copy struct {
	EvalID, ArmID ids.ID
	Arm           string
}

// copyOfIn is the copy record for projectID, read through q: which eval
// and arm made it. A read failure is an error, never "not a copy".
func copyOfIn(ctx context.Context, q database.Queryer, projectID ids.ID) (Copy, bool, error) {
	if projectID.IsZero() {
		return Copy{}, false, nil
	}
	row, err := queries(q).CopyOf(ctx, projectID)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return Copy{}, false, err
	}
	return Copy{EvalID: row.EvalID, ArmID: row.ArmID, Arm: row.Arm}, true, nil
}

// recordCopyIn writes the copy record for a project being created, in the
// creation's own transaction, so no copy exists unmarked.
func recordCopyIn(ctx context.Context, tx *database.Tx, projectID ids.ID, c Copy) error {
	if projectID.IsZero() || c.EvalID.IsZero() || c.ArmID.IsZero() || (c.Arm != ArmBaseline && c.Arm != ArmVariant) {
		return errors.New("evals: a copy record names a project, an eval and an arm")
	}
	if err := queries(tx).RecordCopy(ctx, evalssql.RecordCopyParams{ProjectID: projectID, EvalID: c.EvalID, ArmID: c.ArmID, Arm: c.Arm}); err != nil {
		return fmt.Errorf("recording project %d as a copy for eval %d: %w", projectID, c.EvalID, err)
	}
	return nil
}
