package workflow

import (
	"context"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// ChangeMergedIn observes a receipt only within the Change's admitted project.
func ChangeMergedIn(ctx context.Context, db database.Queryer, projectID, changeID ids.ID) (bool, error) {
	return workflowsql.New(db.SQL()).ChangeMergedReceipt(ctx, workflowsql.ChangeMergedReceiptParams{ChangeID: changeID, ProjectID: projectID})
}
