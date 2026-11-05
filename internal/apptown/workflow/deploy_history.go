package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

// LandingDeployAttempt retains the identity and outcome of one invocation,
// including attempts that stopped before a deployment generation was admitted.
type LandingDeployAttempt struct {
	OutcomeKnown bool   `json:"outcomeKnown"`
	Step         Step   `json:"step"`
	Attempt      int64  `json:"attempt"`
	ChangeID     ids.ID `json:"changeId"`
	ChangeNumber int64  `json:"changeNumber"`
	ChangeTitle  string `json:"changeTitle"`
	RunStartedAt string `json:"runStartedAt"`
}

// LandingDeployAttempts reads scoped Work evidence, without provider effects or
// issuing attempt ownership. Deployment composes these with its release ledger.
func (s Changes) LandingDeployAttempts(ctx context.Context, project ids.ID) ([]LandingDeployAttempt, error) {
	if _, err := requireActor(ctx, s.authority, operationPermission{Action: actionReadEvidence, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).LandingDeployAttempts(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]LandingDeployAttempt, 0, len(rows))
	for _, row := range rows {
		var start struct {
			Attempt int64 `json:"attempt"`
		}
		if err := json.Unmarshal(row.Started, &start); err != nil {
			return nil, fmt.Errorf("reading deploy start: %w", err)
		}
		var finish struct {
			Status   StepState       `json:"status"`
			Evidence json.RawMessage `json:"deployEvidence"`
		}
		step := &Step{ID: row.StepID, RunID: row.RunID, Name: row.Name, Stage: row.Stage, Type: "deploy", Config: row.ConfigJson, Status: StatusRunning, StartedAt: stamp.Of(row.AttemptStartedAt), Evidence: json.RawMessage(`{}`)}
		if string(row.Finished) != "{}" {
			if err := json.Unmarshal(row.Finished, &finish); err != nil {
				return nil, fmt.Errorf("reading deploy finish: %w", err)
			}
			step.Status, step.FinishedAt = finish.Status, stamp.Of(row.AttemptFinishedAt)
			if len(finish.Evidence) > 0 {
				step.Evidence = finish.Evidence
			}
		}
		known := finish.Status != 0
		if start.Attempt == row.CurrentAttempt && row.CurrentStatus != "pending" {
			known = true
			if err := step.Status.Scan(row.CurrentStatus); err != nil {
				return nil, err
			}
			step.Evidence = row.CurrentEvidence
			if row.FinishedAt.Valid {
				step.FinishedAt = stamp.Of(row.FinishedAt.Time)
			}
		}
		out = append(out, LandingDeployAttempt{OutcomeKnown: known, Step: *step, Attempt: start.Attempt, ChangeID: row.ChangeID, ChangeNumber: row.ChangeNumber, ChangeTitle: row.ChangeTitle, RunStartedAt: row.RunStartedAt.Format(time.RFC3339Nano)})
	}
	return out, nil
}
