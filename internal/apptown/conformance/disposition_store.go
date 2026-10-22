package conformance

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/database"
)

// One checked selection serves project management, gates and fleet snapshots.
// Nil/empty project sets return no rows; active means unexpired at at, the
// AppTown's clock. Historic snapshots never pass through the expiry filter.
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
