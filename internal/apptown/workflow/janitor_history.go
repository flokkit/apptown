package workflow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

type janitorLandedChange struct {
	Project      string              `json:"project"`
	PackVersions []adoption.PackPick `json:"packVersions"`
	ChangeID     ids.ID              `json:"changeId"`
	ProjectID    ids.ID              `json:"projectId"`
	Title        string              `json:"title"`
	LandedAt     time.Time           `json:"landedAt"`
	Queue        string              `json:"queue"`
	Packages     []PackageMove       `json:"packages"`
}

type JanitorHistory struct {
	Changes []janitorLandedChange `json:"changes"`
	More    bool                  `json:"more"`
}

// LandedJanitorChanges pages by landing time within an explicit half-open
// window. Visibility is fenced before LIMIT so hidden rows cannot crowd out
// readable history. Detail evidence remains on the Change.
func (s *Service) LandedJanitorChanges(ctx context.Context, since, until time.Time, offset, limit int) (JanitorHistory, error) {
	out := JanitorHistory{Changes: []janitorLandedChange{}}
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return out, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := workflowsql.New(s.db.SQL()).JanitorLandedChanges(ctx, workflowsql.JanitorLandedChangesParams{ProjectIds: visible, Since: since, Until: until, RowOffset: int32(offset), RowLimit: int32(limit + 1)})
	if err != nil {
		return out, err
	}
	out.More = len(rows) > limit
	if out.More {
		rows = rows[:limit]
	}
	for _, row := range rows {
		item := janitorLandedChange{Project: row.Name, PackVersions: []adoption.PackPick{}, ChangeID: row.ID, ProjectID: row.ProjectID, Title: row.Title, LandedAt: row.ClosedAt.Time, Queue: row.Queue, Packages: []PackageMove{}}
		if err := json.Unmarshal(row.Packages, &item.Packages); err != nil {
			return out, err
		}
		if len(row.TargetCandidateJson) > 0 {
			var candidate adoption.Candidate
			if err := json.Unmarshal(row.TargetCandidateJson, &candidate); err != nil {
				return out, err
			}
			item.PackVersions = candidate.Packs
		}
		out.Changes = append(out.Changes, item)
	}
	return out, nil
}
