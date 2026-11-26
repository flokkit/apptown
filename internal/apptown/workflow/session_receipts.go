package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Once an attempt starts, its receipt owns the session identity. Counting back
// one attempt drifts on the second consecutive continue, which may have reused
// an older session. The fallback supports receipts written before session IDs.
func previousAgentSessionIn(ctx context.Context, tx *database.Tx, step *Step) (string, error) {
	q := workflowsql.New(tx.SQL())
	session, err := q.LatestAgentSessionReceipt(ctx, workflowsql.LatestAgentSessionReceiptParams{RunID: step.RunID, StepName: step.Name})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if session != "" {
		return session, nil
	}
	cfg := continuationConfig{}
	if err := json.Unmarshal(step.Config, &cfg); err != nil {
		return "", err
	}
	if cfg.RetryMode == "continue" && cfg.ContinueSession != "" {
		return cfg.ContinueSession, nil
	}
	count, err := q.AgentStartReceiptCount(ctx, workflowsql.AgentStartReceiptCountParams{RunID: step.RunID, StepName: step.Name})
	if err != nil {
		return "", err
	}
	attempts := int(count)
	if attempts == 0 {
		return "", nil
	}
	if cfg.RetryMode == "continue" && attempts > 1 {
		attempts--
	}
	return fmt.Sprintf("step-%d-a%d", step.ID, attempts), nil
}

func nextAgentSession(ctx context.Context, tx *database.Tx, step *Step) (string, error) {
	cfg := continuationConfig{}
	if err := json.Unmarshal(step.Config, &cfg); err != nil {
		return "", err
	}
	if cfg.RetryMode == "continue" {
		session, err := previousAgentSessionIn(ctx, tx, step)
		if err != nil || session != "" {
			return session, err
		}
	}
	attempts, err := workflowsql.New(tx.SQL()).AgentStartReceiptCount(ctx, workflowsql.AgentStartReceiptCountParams{RunID: step.RunID, StepName: step.Name})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("step-%d-a%d", step.ID, attempts+1), nil
}

type continuationConfig struct {
	RetryMode       string `json:"retryMode"`
	ContinueSession string `json:"continueSession"`
}

// keptSessionIn says whether the session a continuing attempt of step would
// resume has kept history: the receipt the next attempt's continuation reads
// (ContinuedTranscriptIn, as the executor's earlierHistory does) names a
// digest. Whether those bytes are still readable is the executor's check.
func keptSessionIn(ctx context.Context, tx *database.Tx, run *Run, step *Step) (bool, error) {
	session, err := previousAgentSessionIn(ctx, tx, step)
	if err != nil || session == "" {
		return false, err
	}
	earlier, err := ContinuedTranscriptIn(ctx, tx, RunRef{ID: run.ID, ProjectID: run.ProjectID}, step.ID, step.Attempt+1, session)
	if err != nil {
		return false, err
	}
	return earlier.Digest != "", nil
}
