package adrs

import (
	"context"
	"encoding/json"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adrs/internal/adrssql"
)

// ExportProjectIn reads a project's ADRs whole, for moving the project to
// another AppTown.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]json.RawMessage, error) {
	return database.JSONRows(adrssql.New(q.SQL()).ExportADRs(ctx, projectID))
}
