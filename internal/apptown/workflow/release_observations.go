package workflow

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// ChangeLabel is display metadata, without a second lifecycle representation.
type ChangeLabel struct {
	ID          ids.ID
	Title, Type string
}

// ReleaseChanges observes Work labels through Delivery's immutable membership
// receipts. Both the release and Change must belong to the admitted project.
// Work does not interpret release states or alter membership. One batch serves
// a release list; unknown/foreign release IDs have empty contents.
func (s *Service) ReleaseChanges(ctx context.Context, project ids.ID, idList []ids.ID) (map[ids.ID][]ChangeLabel, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("release Changes require project scope")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionReadChange, ProjectID: project}); err != nil {
		return nil, err
	}
	return readReleaseChanges(ctx, s.db, project, idList)
}
func readReleaseChanges(ctx context.Context, q database.Queryer, project ids.ID, idList []ids.ID) (map[ids.ID][]ChangeLabel, error) {
	out := make(map[ids.ID][]ChangeLabel, len(idList))
	for _, id := range idList {
		if id.IsZero() {
			return nil, fmt.Errorf("release IDs must be positive")
		}
		out[id] = []ChangeLabel{}
	}
	if len(idList) == 0 {
		return out, ctx.Err()
	}
	rows, err := workflowsql.New(q.SQL()).ReleaseChangeLabels(ctx, workflowsql.ReleaseChangeLabelsParams{ProjectID: project, ReleaseIds: idList})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ReleaseID] = append(out[row.ReleaseID], ChangeLabel{ID: row.ChangeID, Title: row.Title, Type: row.Type})
	}
	return out, nil
}

// ShippedChangesSince uses the strict closing-time cutoff of the release plan.
// A zero time selects all timestamped shipped history. Zero limit is uncapped.
func (s *Service) ShippedChangesSince(ctx context.Context, project ids.ID, after time.Time, limit int) ([]ChangeLabel, error) {
	if project.IsZero() || limit < 0 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("shipped Changes require scope and valid limit")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionListChanges, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).ShippedChangeLabelsMeta(ctx, workflowsql.ShippedChangeLabelsMetaParams{ProjectID: project, ShippedState: ChangeShipped.String(), After: after, RowLimit: int32(limit)})
	if err != nil {
		return nil, database.Missing(err)
	}
	out := make([]ChangeLabel, 0, len(rows))
	for _, row := range rows {
		out = append(out, ChangeLabel{ID: row.ID, Title: row.Title, Type: row.Type})
	}
	return out, nil
}

// LatestSBOMBeforeIn is a scoped metadata-only observation for release displays.
func LatestSBOMBeforeIn(ctx context.Context, q database.Queryer, project ids.ID, before time.Time) (ids.ID, error) {
	if project.IsZero() {
		return ids.Nil, fmt.Errorf("SBOM observation requires project scope")
	}
	rows, err := workflowsql.New(q.SQL()).LatestSBOMIDBeforeMeta(ctx, workflowsql.LatestSBOMIDBeforeMetaParams{ProjectID: project, CreatedAt: before})
	if err != nil {
		return ids.Nil, database.Missing(err)
	}
	if len(rows) == 0 {
		return ids.Nil, nil
	}
	return rows[0], nil
}
