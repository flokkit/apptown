package evals

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RetainsWorkIn participates in Work's automated disposal decision. The caller
// holds the project lock and authorizes cleanup; retained references prevent it.
func RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return evalssql.New(db.SQL()).RetainsAutomatedWork(ctx, evalssql.RetainsAutomatedWorkParams{ChangeID: change, RunIds: runs})
}
