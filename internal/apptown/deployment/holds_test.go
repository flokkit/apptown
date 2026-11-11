package deployment

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
)

// holdWhole admits a live operation holding dep whole, as an in-place
// restore and a take-down do.
func holdWhole(t *testing.T, db *database.DB, dep *Deployment) {
	t.Helper()
	if err := jobs.Migrate(t.Context(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	ops := operations.New(db, nil, nil, nil)
	ops.Register(operations.Kind{Name: "deployment.restore", Capability: access.CapDeploymentDeploy,
		Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			return operations.Outcome{}
		}})
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := ops.AdmitIn(t.Context(), tx, operations.Spec{Kind: "deployment.restore", ProjectID: dep.ProjectID, Subject: SubjectOf(dep), Authority: operations.System("test")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
