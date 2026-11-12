package tasks

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/tasks/internal/taskssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RetainsWorkIn participates in Work's automated disposal decision. The caller
// holds the project lock and authorizes cleanup; retained references prevent it.
func RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return taskssql.New(db.SQL()).RetainsAutomatedWork(ctx, runs)
}
