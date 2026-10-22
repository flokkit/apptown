package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ChangeMergedIn observes a receipt only within the Change's admitted project.
func ChangeMergedIn(ctx context.Context, db database.Queryer, projectID, changeID ids.ID) (bool, error) {
	return workflowsql.New(db.SQL()).ChangeMergedReceipt(ctx, workflowsql.ChangeMergedReceiptParams{ChangeID: changeID, ProjectID: projectID})
}
