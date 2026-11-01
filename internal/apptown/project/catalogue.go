package project

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
)

// catalogue supplies authorization facts before the authorized Project service
// is constructed. It contains no policy, cache or caller-facing project content.
type catalogue struct{ db *database.DB }

func NewCatalogue(db *database.DB) catalogue { return catalogue{db: db} }

// ProjectResources returns complete project scope facts, optionally for one ID.
// Zero selects the catalogue; a missing positive ID yields an empty catalogue.
func (c catalogue) ProjectResources(ctx context.Context, id ids.ID) ([]access.Resource, error) {
	rows, err := projectsql.New(c.db.SQL()).ProjectAuthorityCatalogue(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]access.Resource, 0, len(rows))
	for _, r := range rows {
		out = append(out, access.Resource{Type: "project", ID: r.ID.String(), ProjectID: r.ID, ProjectName: r.Name, Group: r.OwnerGroup})
	}
	return out, nil
}
