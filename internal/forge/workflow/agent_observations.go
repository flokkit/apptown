package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func latestFeedbackIn(ctx context.Context, q database.Queryer, ref RunRef) (*Step, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, fmt.Errorf("feedback requires Run scope")
	}
	row, err := workflowsql.New(q.SQL()).LatestFeedbackStep(ctx, workflowsql.LatestFeedbackStepParams{RunID: ref.ID, ProjectID: ref.ProjectID, FailedState: StatusFail.String()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return stepResult(row, err)
}

// PersonFeedback is what a person last asked of a run's work: a send-back
// note (apptown retry -m, a change sent back) or an approver's rejection
// comment, and who said it.
type PersonFeedback struct{ Note, By string }

// LatestPersonFeedbackIn reads a run's latest PersonFeedback; nil when no
// person has sent the work back. Machine verdicts (a reviewer's, a gate's)
// are not a person's and are never returned.
func LatestPersonFeedbackIn(ctx context.Context, q database.Queryer, ref RunRef) (*PersonFeedback, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, fmt.Errorf("feedback requires Run scope")
	}
	row, err := workflowsql.New(q.SQL()).LatestPersonFeedback(ctx, workflowsql.LatestPersonFeedbackParams{RunID: ref.ID, ProjectID: ref.ProjectID, FailedState: StatusFail.String()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, database.QueryError(err, "reading a person's latest feedback on the run")
	}
	return &PersonFeedback{Note: row.Note, By: row.Who}, nil
}

type AgentSession struct {
	Dispatches int
	ID         string
}

// AgentSessionIn preserves the recorded conversation first, including completed
// history continuations. Unrecorded history uses its dispatch-count convention;
// it must not be renumbered from the newer Step attempt field.
func AgentSessionIn(ctx context.Context, q database.Queryer, ref RunRef, step ids.ID, mode RetryMode) (AgentSession, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() || step.IsZero() || !mode.valid() {
		return AgentSession{}, fmt.Errorf("session observation requires scope and valid retry mode")
	}
	row, err := workflowsql.New(q.SQL()).AgentSessionObservation(ctx, workflowsql.AgentSessionObservationParams{StepID: step, RunID: ref.ID, ProjectID: ref.ProjectID})
	if err != nil {
		return AgentSession{}, err
	}
	out := AgentSession{Dispatches: int(row.Dispatches), ID: row.SessionID}
	if out.ID == "" {
		attempt := out.Dispatches
		if mode == RetryContinue && attempt > 1 {
			attempt--
		}
		out.ID = fmt.Sprintf("step-%d-a%d", step, attempt)
	}
	return out, nil
}
