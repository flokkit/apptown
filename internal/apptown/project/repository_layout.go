package project

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
)

// RepositoryOwner is an offline layout observation. Retained ownership survives
// project removal; a completed deletion is not permission to adopt a reused slug.
type RepositoryOwner struct {
	Ref           RepositoryRef
	DefaultBranch string
	Lifecycle     string
	Retained      bool
}

// RepositoryOwnersIn reads all current and pending retained owners, without a
// display page limit. The migration also asks SCM for initialized-main evidence.
func RepositoryOwnersIn(ctx context.Context, q database.Queryer) ([]RepositoryOwner, error) {
	rows, err := projectsql.New(q.SQL()).RepositoryLayoutOwners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RepositoryOwner, 0, len(rows))
	for _, row := range rows {
		out = append(out, RepositoryOwner{Ref: RepositoryRef{ID: row.ID, Slug: row.Slug}, DefaultBranch: row.DefaultBranch, Lifecycle: row.Lifecycle, Retained: row.Retained})
	}
	return out, nil
}
