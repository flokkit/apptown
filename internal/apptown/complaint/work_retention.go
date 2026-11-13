package complaint

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/complaint/internal/complaintsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RetainsWorkIn keeps the work referenced by this owner's records.
func RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return complaintsql.New(db.SQL()).RetainsAutomatedWork(ctx, complaintsql.RetainsAutomatedWorkParams{ChangeID: change, RunIds: runs})
}
