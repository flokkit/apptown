package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// ApprovalInboxItem combines canonical records with display labels. Observing a
// pending approval does not establish eligibility to vote on it.
type ApprovalInboxItem struct {
	StepContext
	ProjectSlug, ProjectName, ChangeTitle string
}

func (s *Service) ApprovalInbox(ctx context.Context, project ids.ID) ([]ApprovalInboxItem, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	projects, err := s.visibleProjects(ctx, actionReadEvidence, project)
	if err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).ApprovalInbox(ctx, workflowsql.ApprovalInboxParams{Projects: projects, RunningState: RunRunning.String(), PendingState: StatusPending.String(), HumanState: StatusNeedsHuman.String()})
	if err != nil {
		return nil, err
	}
	out := make([]ApprovalInboxItem, 0, len(rows))
	for _, row := range rows {
		run, err := runRecord(row.Run, nil)
		if err != nil {
			return nil, err
		}
		step, err := stepRecord(row.Step)
		if err != nil {
			return nil, err
		}
		out = append(out, ApprovalInboxItem{StepContext: StepContext{Run: run, Step: step}, ProjectSlug: row.ProjectSlug, ProjectName: row.ProjectName, ChangeTitle: row.ChangeTitle})
	}
	return out, nil
}
