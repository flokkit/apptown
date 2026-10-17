package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"
)

// TimingWindow selects a specific historical Run or a workflow/time window.
// A Run ID takes precedence over the other filters, including zero/missing IDs.
type TimingWindow struct {
	RunID    *ids.ID
	Workflow string
	Since    time.Time
}

// RunTiming and StepTiming are observations for statistics, never execution claims.
type RunTiming struct {
	ID        ids.ID
	Workflow  string
	CreatedAt time.Time
	Steps     []StepTiming
}
type StepTiming struct {
	Stage, Name, Type     string
	Status                StepState
	StartedAt, FinishedAt stamp.Time
	Evidence              json.RawMessage
	Reworks               int
}

func (s *Service) Timings(ctx context.Context, in TimingWindow) ([]RunTiming, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	var projects []ids.ID
	if in.RunID != nil {
		if in.RunID.IsZero() {
			return []RunTiming{}, nil
		}
		project, _, err := s.runScope(ctx, *in.RunID, actionReadEvidence)
		if err != nil {
			return nil, err
		}
		projects = []ids.ID{project}
	} else {
		var err error
		projects, err = s.visibleProjects(ctx, actionReadEvidence, ids.Nil)
		if err != nil {
			return nil, err
		}
	}
	return readTimingsIn(ctx, s.db, projects, in)
}

// readTimingsIn reads the ledger in one statement, through the admitted project
// set and supplied connection. An empty set selects nothing, never the instance.
func readTimingsIn(ctx context.Context, q database.Queryer, projects []ids.ID, in TimingWindow) ([]RunTiming, error) {
	for _, id := range projects {
		if id.IsZero() {
			return nil, fmt.Errorf("timing requires positive project IDs")
		}
	}
	if len(projects) == 0 {
		return []RunTiming{}, ctx.Err()
	}
	var selectedID ids.ID // none: every run
	if in.RunID != nil {
		selectedID = *in.RunID
	}
	rows, err := workflowsql.New(q.SQL()).RunTimingReport(ctx, workflowsql.RunTimingReportParams{RunID: selectedID, WorkflowName: in.Workflow, Since: in.Since, ProjectIds: projects})
	if err != nil {
		return nil, err
	}
	out := []RunTiming{}
	for _, row := range rows {
		run := RunTiming{ID: row.RunID, Workflow: row.WorkflowName, CreatedAt: row.CreatedAt}
		if len(out) == 0 || out[len(out)-1].ID != run.ID {
			out = append(out, run)
		}
		if !row.StepID.IsZero() {
			state, err := ParseStepState(row.Status)
			if err != nil {
				return nil, err
			}
			step := StepTiming{Stage: row.Stage, Name: row.Name, Type: row.Type, Status: state,
				StartedAt: stamp.Time{NullTime: row.StartedAt}, FinishedAt: stamp.Time{NullTime: row.FinishedAt},
				Evidence: row.EvidenceJson, Reworks: int(row.Reworks)}
			out[len(out)-1].Steps = append(out[len(out)-1].Steps, step)
		}
	}
	return out, nil
}
