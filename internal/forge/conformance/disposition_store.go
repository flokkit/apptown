package conformance

import (
	"context"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
)

// One checked selection serves project management, gates and fleet snapshots.
// Nil/empty project sets return no rows; active means unexpired at at, the
// forge's clock. Historic snapshots never pass through the expiry filter.
func readDispositions(ctx context.Context, db database.Queryer, projects []ids.ID, active bool, at time.Time) ([]Disposition, error) {
	rows, err := queries(db).DispositionsForProjects(ctx, conformancesql.DispositionsForProjectsParams{
		Projects: projects, Active: active, At: at,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Disposition, 0, len(rows))
	for _, r := range rows {
		d, err := disposition(r)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
