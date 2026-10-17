package project

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/project/internal/projectsql"
)

func (s *Projects) AuthorityResource(ctx context.Context, id ids.ID) (access.Resource, error) {
	row, err := projectsql.New(s.db.SQL()).ProjectByID(ctx, id)
	if err != nil {
		return access.Resource{}, err
	}
	return access.Resource{Type: "project", ID: id.String(), ProjectID: id, ProjectName: row.Name, Group: row.OwnerGroup}, nil
}
