package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

// projectActivity is a bulk observation, never permission to start or remove work.
type projectActivity struct {
	Changes, ActiveItems, DoneRuns, RunningRuns int
	OpenChanges, OpenUplifts                    int
	LastRunAt                                   stamp.Time
	LastChangeTitle                             string
}

func (s *Service) ProjectActivity(ctx context.Context, project ids.ID) (map[ids.ID]projectActivity, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	projects, err := s.visibleProjects(ctx, actionReadRun, project)
	if err != nil {
		return nil, err
	}
	return readProjectActivity(ctx, s.db, projects)
}

func readProjectActivity(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID]projectActivity, error) {
	out := make(map[ids.ID]projectActivity, len(projects))
	if len(projects) == 0 {
		return out, ctx.Err()
	}
	rows, err := workflowsql.New(q.SQL()).ProjectActivityReport(ctx, workflowsql.ProjectActivityReportParams{
		ProjectIds: projects, RunningStatus: RunRunning.String(), DoneStatus: RunDone.String(),
		InProgressState: ChangeInProgress.String(), LandingState: ChangeLanding.String(),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		activity := projectActivity{LastChangeTitle: row.LastChangeTitle, Changes: int(row.Changes), ActiveItems: int(row.ActiveItems), DoneRuns: int(row.DoneRuns), RunningRuns: int(row.RunningRuns), OpenChanges: int(row.OpenChanges), OpenUplifts: int(row.OpenUplifts)}
		if row.HasLastRunAt {
			activity.LastRunAt = stamp.Time{NullTime: sql.NullTime{Time: row.LastRunAt, Valid: true}}
		}
		out[row.ID] = activity
	}
	return out, nil
}

type workflowUsage struct {
	Name      string
	Runs      int
	LastRunAt string
}

func (s *Service) WorkflowUsage(ctx context.Context, project ids.ID) (map[string]workflowUsage, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("workflow usage requires project scope")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionReadRun, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).WorkflowUsageReport(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]workflowUsage, len(rows))
	for _, v := range rows {
		usage := workflowUsage{Name: v.WorkflowName, Runs: int(v.Runs)}
		usage.LastRunAt = v.LastRunAt.Format(time.RFC3339Nano)
		out[v.WorkflowName] = usage
	}
	return out, nil
}

// ProjectActivityFor bounds the work read to requested projects after checking
// run visibility. A caller's selection can narrow, never expand, that scope.
func (s *Service) ProjectActivityFor(ctx context.Context, requested []ids.ID) (map[ids.ID]projectActivity, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	idList, err := s.visibleProjects(ctx, actionReadRun, ids.Nil)
	if err != nil {
		return nil, err
	}
	wanted := make(map[ids.ID]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	selected := make([]ids.ID, 0, len(requested))
	for _, id := range idList {
		if wanted[id] {
			selected = append(selected, id)
		}
	}
	return readProjectActivity(ctx, s.db, selected)
}
