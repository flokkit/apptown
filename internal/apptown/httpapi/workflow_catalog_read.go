package httpapi

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type libraryCatalog struct {
	workflows []wow.WorkflowListing
}

// The catalog uses one bulk read regardless of the number of workflows.
func loadLibraryCatalog(ctx context.Context, db database.Queryer) (libraryCatalog, error) {
	var c libraryCatalog
	records, err := wow.ReadWorkflowCatalogIn(ctx, db)
	if err != nil {
		return c, err
	}
	c.workflows = records.Workflows
	return c, nil
}
