package adrs

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/database"
)

// ExportProjectIn reads a project's ADRs whole, for moving the project to
// another AppTown.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]json.RawMessage, error) {
	return database.JSONRows(adrssql.New(q.SQL()).ExportADRs(ctx, projectID))
}
