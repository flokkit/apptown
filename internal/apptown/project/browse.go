package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
)

// Browse bounds project display rows. IDs optionally narrow a selection made by
// another owner; they never grant access. Their order is preserved when present.
type Browse struct {
	Limit, Offset int
	Query         string
	IDs           []ids.ID
}
type Page struct {
	Projects []Project
	Total    int64
}

func (s *Projects) Browse(ctx context.Context, in Browse) (Page, error) {
	if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
		return Page{}, fmt.Errorf("project page requires limit 1–100 and a nonnegative offset")
	}
	idList, err := s.authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return Page{}, err
	}
	if in.IDs != nil {
		allowed := make(map[ids.ID]bool, len(idList))
		for _, id := range idList {
			allowed[id] = true
		}
		idList = make([]ids.ID, 0, len(in.IDs))
		for _, id := range in.IDs {
			if allowed[id] {
				idList = append(idList, id)
				delete(allowed, id)
			}
		}
	}
	out := Page{Projects: []Project{}}
	if len(idList) == 0 {
		return out, ctx.Err()
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	q := projectsql.New(tx.SQL())
	query := strings.ToLower(strings.TrimSpace(in.Query))
	out.Total, err = q.VisibleProjectCount(ctx, projectsql.VisibleProjectCountParams{ProjectIds: idList, Query: query})
	if err != nil {
		return Page{}, err
	}
	rows, err := q.ProjectPage(ctx, projectsql.ProjectPageParams{ProjectIds: idList, Query: query, Ordered: in.IDs != nil, RowLimit: int64(in.Limit), RowOffset: int64(in.Offset)})
	if err != nil {
		return Page{}, err
	}
	for _, r := range rows {
		out.Projects = append(out.Projects, record(r))
	}
	if err := tx.Commit(); err != nil {
		return Page{}, err
	}
	return out, nil
}
