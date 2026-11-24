package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids"
	"time"
)

// recentNeedUpdate is a released refresh whose engine opened its volume.
type recentNeedUpdate struct {
	ID            ids.ID
	ProjectID     ids.ID
	Project, Slug string
	Number        int64
	At            time.Time
	Moves         []NeedMove
}

// RecentNeedMoves returns visible successful refreshes in a half-open time window.
func (s *Service) RecentNeedMoves(ctx context.Context, since, until time.Time, limit int) ([]recentNeedUpdate, error) {
	authority, ok := s.authority.(access.ReadAuthorizer)
	if !ok {
		return nil, access.ErrAuthorityUnavailable
	}
	visible, err := authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	rows, err := deploymentsql.New(s.db.SQL()).RecentNeedMoves(ctx, deploymentsql.RecentNeedMovesParams{ProjectIds: visible, Since: sql.NullTime{Time: since, Valid: true}, Until: sql.NullTime{Time: until, Valid: true}, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := []recentNeedUpdate{}
	for _, r := range rows {
		var moves []NeedMove
		if err := json.Unmarshal(r.NeedMoves, &moves); err != nil {
			return nil, err
		}
		out = append(out, recentNeedUpdate{r.ID, r.ProjectID, r.ProjectName, r.ProjectSlug, r.Number, r.ReleasedAt.Time, moves})
	}
	return out, nil
}
