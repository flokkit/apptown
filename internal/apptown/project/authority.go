package project

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

func (s *Projects) AuthorityResource(ctx context.Context, id ids.ID) (access.Resource, error) {
	return AuthorityResourceIn(ctx, s.db, id)
}

// AuthorityResourceIn resolves the current containing scopes in an owner transaction.
func AuthorityResourceIn(ctx context.Context, q database.Queryer, id ids.ID) (access.Resource, error) {
	row, err := projectsql.New(q.SQL()).ProjectByID(ctx, id)
	if err != nil {
		err = database.Missing(err)
		if fault.KindOf(err) != fault.NotFound {
			err = fault.Wrap(fault.Unavailable, err)
		}
		return access.Resource{}, err
	}
	return access.Resource{Type: "project", ID: id.String(), ProjectID: id, ProjectName: row.Name, Group: row.OwnerGroup}, nil
}
