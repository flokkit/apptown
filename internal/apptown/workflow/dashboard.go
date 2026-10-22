package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type PendingApproval struct {
	StepID   ids.ID `json:"stepId"`
	StepName string `json:"stepName"`
	Stage    string `json:"stage"`
	RunID    ids.ID `json:"runId"`
	ChangeID ids.ID `json:"changeId"`
	// ChangeNumber is the change's number within its project; 0 for a run
	// with no change.
	ChangeNumber int64  `json:"changeNumber"`
	ItemTitle    string `json:"itemTitle"`
	ProjectSlug  string `json:"projectSlug"`
	ProjectName  string `json:"projectName"`
}
type ActiveRun struct {
	RunID        ids.ID `json:"runId"`
	RunNumber    int64  `json:"runNumber"`
	ChangeID     ids.ID `json:"changeId"`
	ChangeNumber int64  `json:"changeNumber"`
	ItemTitle    string `json:"itemTitle"`
	Workflow     string `json:"workflow"`
	Stage        string `json:"stage"`
	ProjectSlug  string `json:"projectSlug"`
	// State and StateDetail are the run's activity in the words the change
	// page uses ("agent working: implement"); the stage name alone is the
	// workflow's internal label.
	State       ActivityState `json:"state"`
	StateDetail string        `json:"stateDetail,omitempty"`
}
type Dashboard struct {
	PendingApprovals []PendingApproval
	ActiveRuns       []ActiveRun
}

// Dashboard partitions running work at one snapshot, so a concurrently settled
// approval cannot put a run in both sections (or neither). Visibility precedes
// ordering and the active-run limit; every association retains project scope.
func (s *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return Dashboard{}, err
	}
	projects, err := s.visibleProjects(ctx, actionReadRun, ids.Nil)
	if err != nil {
		return Dashboard{}, err
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readDashboard(ctx, tx, projects, s.wait)
	if err != nil {
		return Dashboard{}, err
	}
	if err := tx.Commit(); err != nil {
		return Dashboard{}, err
	}
	return out, nil
}
func readDashboard(ctx context.Context, q database.Queryer, projects []ids.ID, wait SlotWait) (Dashboard, error) {
	out := Dashboard{PendingApprovals: []PendingApproval{}, ActiveRuns: []ActiveRun{}}
	if len(projects) == 0 {
		return out, ctx.Err()
	}
	records := workflowsql.New(q.SQL())
	pending, err := records.DashboardPendingApprovals(ctx, workflowsql.DashboardPendingApprovalsParams{ProjectIds: projects, Pending: StatusPending.String(), NeedsHuman: StatusNeedsHuman.String(), Running: RunRunning.String()})
	if err != nil {
		return Dashboard{}, err
	}
	for _, row := range pending {
		out.PendingApprovals = append(out.PendingApprovals, PendingApproval{StepID: row.StepID, StepName: row.StepName, Stage: row.Stage, RunID: row.RunID, ChangeID: row.ChangeID, ChangeNumber: row.ChangeNumber, ItemTitle: row.ItemTitle, ProjectSlug: row.ProjectSlug, ProjectName: row.ProjectName})
	}
	active, err := records.DashboardActiveRuns(ctx, workflowsql.DashboardActiveRunsParams{ProjectIds: projects, Running: RunRunning.String(), Pending: StatusPending.String(), NeedsHuman: StatusNeedsHuman.String()})
	if err != nil {
		return Dashboard{}, err
	}
	for _, row := range active {
		out.ActiveRuns = append(out.ActiveRuns, ActiveRun{RunID: row.RunID, RunNumber: row.RunNumber, ChangeID: row.ChangeID, ChangeNumber: row.ChangeNumber, ItemTitle: row.ItemTitle, Workflow: row.WorkflowName, Stage: row.CurrentStage, ProjectSlug: row.ProjectSlug})
	}
	idList := make([]ids.ID, 0, len(out.ActiveRuns))
	for _, run := range out.ActiveRuns {
		idList = append(idList, run.RunID)
	}
	graph, err := readWorkflowActivity(ctx, q, idList)
	if err != nil {
		return Dashboard{}, err
	}
	for i := range out.ActiveRuns {
		activity, err := graph.classify(out.ActiveRuns[i].RunID, wait, make(map[ids.ID]bool))
		if err != nil {
			return Dashboard{}, err
		}
		out.ActiveRuns[i].State, out.ActiveRuns[i].StateDetail = activity.State, activity.Detail
	}
	return out, nil
}
