package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project"
	"math"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"

	"github.com/flokkit/apptown/internal/database"
)

// Work queries own list selection and derived activity. Their only runtime
// collaborator observes a slot queue; it cannot read or mutate Work storage.
type queries struct {
	db   *database.DB
	wait SlotWait
	// automatic: the step kinds that run without a person, which a step
	// run accepts again (with assertions); nil for reads offering no acts.
	automatic func(string) bool
}

type RunQuery struct {
	projects    []ids.ID // service-owned visibility fence; nil only for private queries
	ProjectID   ids.ID
	ProjectSlug string
	State       RunState // zero selects all lifecycle states
	RootsOnly   bool
	Limit       int // zero is uncapped, used by the project overview
}
type RunSummary struct {
	ID              ids.ID        `json:"id"`
	Number          int64         `json:"number"` // within its project
	Status          RunState      `json:"status"`
	CurrentStage    string        `json:"currentStage"`
	WorkflowName    string        `json:"workflowName"`
	WorkflowVersion int           `json:"workflowVersion"`
	Kind            string        `json:"kind"`
	ChangeID        ids.ID        `json:"changeId"`
	ChangeNumber    int64         `json:"changeNumber"` // 0 when the run has no change
	ItemTitle       string        `json:"itemTitle"`
	ProjectSlug     string        `json:"projectSlug"`
	State           ActivityState `json:"state,omitempty"`
	StateDetail     string        `json:"stateDetail,omitempty"`
	CreatedAt       string        `json:"createdAt"`
	UpdatedAt       string        `json:"updatedAt"`
}

func (q queries) list(ctx context.Context, query RunQuery) ([]RunSummary, error) {
	tx, err := q.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := readWorkflowSummaries(ctx, tx, query, q.wait)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rows, nil
}

func readWorkflowSummaries(ctx context.Context, db database.Queryer, query RunQuery, wait SlotWait) ([]RunSummary, error) {
	if query.Limit < 0 || query.Limit > math.MaxInt32 {
		return nil, fmt.Errorf("invalid workflow list bounds")
	}
	if query.ProjectSlug != "" {
		id, err := project.IDBySlugIn(ctx, db, query.ProjectSlug)
		if errors.Is(err, sql.ErrNoRows) {
			return []RunSummary{}, nil
		}
		if err != nil {
			return nil, err
		}
		if !query.ProjectID.IsZero() && query.ProjectID != id {
			return []RunSummary{}, nil
		}
		query.ProjectID = id
	}
	status := ""
	if query.State != 0 {
		status = query.State.String()
		if _, err := ParseRunState(status); err != nil {
			return nil, err
		}
	}
	stored, err := workflowsql.New(db.SQL()).WorkflowSummaries(ctx, workflowsql.WorkflowSummariesParams{ProjectID: query.ProjectID, Status: status, RootsOnly: query.RootsOnly, ProjectIds: query.projects, LimitCount: int32(query.Limit)})
	if err != nil {
		return nil, err
	}
	rows := make([]RunSummary, 0, len(stored))
	for _, row := range stored {
		state, err := ParseRunState(row.Status)
		if err != nil {
			return nil, err
		}
		rows = append(rows, RunSummary{ID: row.ID, Number: row.Number, Status: state, CurrentStage: row.CurrentStage, WorkflowName: row.WorkflowName, WorkflowVersion: int(row.WorkflowVersion), Kind: row.Kind, ChangeID: row.ChangeID, ChangeNumber: row.ChangeNumber, ItemTitle: row.ItemTitle, ProjectSlug: row.ProjectSlug, CreatedAt: row.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.Format(time.RFC3339Nano)})
	}
	idList := make([]ids.ID, 0, len(rows))
	for _, row := range rows {
		if row.Status == RunRunning {
			idList = append(idList, row.ID)
		}
	}
	graph, err := readWorkflowActivity(ctx, db, idList)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Status != RunRunning {
			continue
		}
		activity, err := graph.classify(rows[i].ID, wait, make(map[ids.ID]bool))
		if err != nil {
			return nil, err
		}
		rows[i].State, rows[i].StateDetail = activity.State, activity.Detail
	}
	return rows, nil
}

// UNION visits each stored run once, even with corrupt cyclic parent links.
// Recursion follows the current parent call attempt in the same project, not an
// arbitrary childRunId supplied in evidence. The model also checks that evidence
// selects an owned child before allowing its activity to bubble up.
func readWorkflowActivity(ctx context.Context, db database.Queryer, idList []ids.ID) (workflowActivityGraph, error) {
	graph := make(workflowActivityGraph)
	if len(idList) == 0 {
		return graph, nil
	}
	rows, err := workflowsql.New(db.SQL()).WorkflowActivity(ctx, workflowsql.WorkflowActivityParams{Ids: idList, StepRunning: StatusRunning.String(), RunRunning: RunRunning.String()})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		state, err := ParseRunState(row.Status)
		if err != nil {
			return nil, err
		}
		node := graph[row.ID]
		if node == nil {
			node = &workflowActivityRun{id: row.ID, projectID: row.ProjectID, parentID: row.ParentRunID, parentStepID: row.ParentStepID, parentAttempt: row.ParentStepAttempt, state: state, abandonmentReason: row.AbandonmentReason, awaitingAcceptance: row.AwaitingAcceptance}
			if node.stage, node.checks, err = stageFacts(row.StageDeclaration, row.StageChecks); err != nil {
				return nil, fmt.Errorf("run %d stage facts: %w", row.ID, err)
			}
			graph[row.ID] = node
		}
		if !row.StepID.IsZero() {
			stepState, err := parseStepState(row.StepStatus)
			if err != nil {
				return nil, err
			}
			node.steps = append(node.steps, workflowActivityStep{id: row.StepID, attempt: row.StepAttempt, name: row.StepName, kind: row.StepType, override: row.StepOverrideBy, state: stepState, evidence: rawJSON(string(row.Evidence)), finished: row.StepFinishedAt})
		}
	}
	return graph, nil
}
