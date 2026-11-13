package adrs

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RetainsWorkIn keeps the work referenced by this owner's records.
func RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return adrssql.New(db.SQL()).RetainsAutomatedWork(ctx, change)
}
