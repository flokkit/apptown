package conformance

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RetainsWorkIn participates in Work's automated disposal decision. The caller
// holds the project lock and authorizes cleanup; retained references prevent it.
func RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return conformancesql.New(db.SQL()).RetainsAutomatedWork(ctx, conformancesql.RetainsAutomatedWorkParams{ChangeID: change, RunIds: runs})
}
