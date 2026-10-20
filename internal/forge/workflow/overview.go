package workflow

import (
	"context"
	"fmt"
	"math"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/stamp"
)

// visibleProjects is shared by caller-facing lists after Identify. The final
// query must retain these IDs as a scope fence, including on instance-wide reads.
func (s *Service) visibleProjects(ctx context.Context, action commandAction, project ids.ID) ([]ids.ID, error) {
	if false {
		return nil, fmt.Errorf("invalid project scope")
	}
	if !project.IsZero() {
		if _, err := s.require(ctx, operationPermission{Action: action, ProjectID: project}); err != nil {
			return nil, err
		}
		return []ids.ID{project}, nil
	}
	return s.authority.VisibleProjects(ctx, action)
}

type HistoryQuery struct {
	ProjectID ids.ID
	Limit     int
}

type ChangeHistory struct {
	CompletedRuns int         `json:"completedRuns"`
	Merged        bool        `json:"merged"`
	ChangeID      ids.ID      `json:"changeId"`
	ChangeNumber  int64       `json:"changeNumber"` // within its project
	Title         string      `json:"title"`
	Type          string      `json:"type"`
	State         ChangeState `json:"state"`
	ClosedAs      string      `json:"closedAs,omitempty"` // as Change.ClosedAs
	Project       string      `json:"project,omitempty"`
	Workflows     string      `json:"workflows"`
	Runs          int         `json:"runs"`
	When          string      `json:"when"`
	TookS         int64       `json:"tookS"`
}

func (s *Service) ChangeHistory(ctx context.Context, in HistoryQuery) ([]ChangeHistory, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if in.Limit < 0 || in.Limit > math.MaxInt32 {
		return nil, fmt.Errorf("invalid history limit")
	}
	projects, err := s.visibleProjects(ctx, actionListChanges, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return []ChangeHistory{}, nil
	}
	return readChangeHistory(ctx, s.db, in, projects)
}

func readChangeHistory(ctx context.Context, q database.Queryer, in HistoryQuery, projects []ids.ID) ([]ChangeHistory, error) {
	rows, err := workflowsql.New(q.SQL()).ChangeHistory(ctx, workflowsql.ChangeHistoryParams{ProjectID: in.ProjectID, Done: RunDone.String(), ProjectIds: projects, Shipped: ChangeShipped.String(), Abandoned: ChangeAbandoned.String(), LimitCount: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ChangeHistory, 0, len(rows))
	for _, row := range rows {
		state, err := ParseChangeState(row.State)
		if err != nil {
			return nil, err
		}
		out = append(out, ChangeHistory{ChangeID: row.ID, ChangeNumber: row.Number, Title: row.Title, Type: row.Type, State: state, Project: row.Project, Workflows: row.Workflows, Runs: int(row.Runs), When: row.ActivityAt.Format(time.RFC3339Nano), TookS: row.TookS, CompletedRuns: int(row.Completed), Merged: row.Merged, ClosedAs: row.ClosedAs})
	}
	return out, nil
}

type OpenChange struct {
	ID     ids.ID
	Number int64 // within its project
	Title  string
	State  ChangeState
}

func (s *Service) OpenChangesWithoutRuns(ctx context.Context, project ids.ID, limit int) ([]OpenChange, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if project.IsZero() || limit < 0 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("open Changes require a project and valid limit")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionListChanges, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).OpenChangesWithoutRunsMeta(ctx, workflowsql.OpenChangesWithoutRunsMetaParams{ProjectID: project, InProgress: ChangeInProgress.String(), Landing: ChangeLanding.String(), Running: RunRunning.String(), LimitCount: int32(limit)})
	if err != nil {
		return nil, database.Missing(err)
	}
	out := make([]OpenChange, 0, len(rows))
	for _, row := range rows {
		state, err := ParseChangeState(row.State)
		if err != nil {
			return nil, err
		}
		out = append(out, OpenChange{ID: row.ID, Number: row.Number, Title: row.Title, State: state})
	}
	return out, nil
}

type RunningStep struct {
	Project                 string
	RunID, ChangeID, StepID ids.ID
	Title, Name, Type       string
	StartedAt               stamp.Time
}

func (s *Service) RunningSteps(ctx context.Context) ([]RunningStep, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	projects, err := s.visibleProjects(ctx, actionReadRun, ids.Nil)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return []RunningStep{}, nil
	}
	rows, err := workflowsql.New(s.db.SQL()).RunningSteps(ctx, workflowsql.RunningStepsParams{ProjectIds: projects, Running: StatusRunning.String()})
	if err != nil {
		return nil, err
	}
	out := make([]RunningStep, 0, len(rows))
	for _, row := range rows {
		out = append(out, RunningStep{Project: row.Project, RunID: row.RunID, ChangeID: row.ChangeID, StepID: row.StepID, Title: row.Title, Name: row.Name, Type: row.Type, StartedAt: stamp.Time{NullTime: row.StartedAt}})
	}
	return out, nil
}

// ExecutingSteps is every step executing now, in every project: the work a
// restart of AppTown would interrupt. A run waiting on a person, or a step
// queued for a slot, is not here; it survives a restart as it is. The read is
// about the whole instance, so a caller who cannot read every project's runs
// is refused rather than shown a partial list that could read as idle.
func (s *Service) ExecutingSteps(ctx context.Context) ([]RunningStep, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadRun}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).ExecutingSteps(ctx, StatusRunning.String())
	if err != nil {
		return nil, err
	}
	out := make([]RunningStep, 0, len(rows))
	for _, row := range rows {
		out = append(out, RunningStep{Project: row.Project, RunID: row.RunID, ChangeID: row.ChangeID, StepID: row.StepID, Title: row.Title, Name: row.Name, Type: row.Type, StartedAt: stamp.Time{NullTime: row.StartedAt}})
	}
	return out, nil
}

// OpenRun is a run not yet finished, anywhere on this AppTown.
type OpenRun struct {
	ID      ids.ID
	Number  int64
	Project string // slug
}

// OpenRunsEverywhere lists every run still open, for an instance operation
// that must not change repositories under one (apptown upgrade).
func (s *Service) OpenRunsEverywhere(ctx context.Context) ([]OpenRun, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadRun}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).OpenRunsEverywhere(ctx, RunRunning.String())
	if err != nil {
		return nil, err
	}
	out := make([]OpenRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, OpenRun{ID: row.ID, Number: row.Number, Project: row.Project})
	}
	return out, nil
}

// OpenChangeBranches is the recorded branch of each unfinished change of a
// project, keyed by change number.
func (s *Service) OpenChangeBranches(ctx context.Context, project ids.ID) (map[int64]string, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionListChanges, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).OpenChangeBranches(ctx, workflowsql.OpenChangeBranchesParams{ProjectID: project, Proposed: ChangeProposed.String(), InProgress: ChangeInProgress.String(), Landing: ChangeLanding.String()})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, row := range rows {
		out[row.Number] = row.Branch
	}
	return out, nil
}
