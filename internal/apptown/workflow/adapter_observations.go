package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// ChangeRecords is a visible subset of requested IDs, ordered by ID. Project
// zero selects all visible projects; a positive project explicitly narrows it. Missing and
// unauthorized Changes are absent, as in other lists; read failures are errors.
func (s *Service) ChangeRecords(ctx context.Context, project ids.ID, idList []ids.ID) ([]*Change, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if false {
		return nil, fmt.Errorf("invalid project scope")
	}
	for _, id := range idList {
		if id.IsZero() {
			return nil, fmt.Errorf("Change IDs must be positive")
		}
	}
	if len(idList) == 0 {
		return []*Change{}, nil
	}
	projects, err := s.visibleProjects(ctx, actionReadChange, project)
	if err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).VisibleChanges(ctx, workflowsql.VisibleChangesParams{ProjectIds: projects, Ids: idList})
	if err != nil {
		return nil, err
	}
	changes := make([]*Change, 0, len(rows))
	for _, row := range rows {
		ch, err := changeRecord(row, nil)
		if err != nil {
			return nil, err
		}
		changes = append(changes, ch)
	}
	return changes, nil
}

// PendingUpliftAdoptionsIn is the startup owner's minimal repair selection.
// Work owns lifecycle selection; WoW still owns validating/applying a revision.
func PendingUpliftAdoptionsIn(ctx context.Context, q database.Queryer) ([]ChangeRef, error) {
	rows, err := workflowsql.New(q.SQL()).PendingUpliftAdoptions(ctx, ChangeShipped.String())
	if err != nil {
		return nil, err
	}
	refs := make([]ChangeRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, ChangeRef{ID: row.ID, ProjectID: row.ProjectID})
	}
	return refs, nil
}

func RunningWorkflowCountIn(ctx context.Context, q database.Queryer, project ids.ID, name string) (int, error) {
	if project.IsZero() || name == "" {
		return 0, fmt.Errorf("running workflow count requires project and workflow")
	}
	count, err := workflowsql.New(q.SQL()).RunningWorkflowCount(ctx, workflowsql.RunningWorkflowCountParams{ProjectID: project, WorkflowName: name, Status: RunRunning.String()})
	if err != nil {
		return 0, err
	}
	return int(count[0]), nil
}
