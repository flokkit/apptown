package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

func readStageChecks(ctx context.Context, db database.Queryer, runID ids.ID, stage string) ([]Check, error) {
	raw, err := workflowsql.New(db.SQL()).LatestStageChecks(ctx, workflowsql.LatestStageChecksParams{RunID: runID, Stage: stage})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checks []Check
	err = json.Unmarshal([]byte(raw), &checks)
	return checks, err
}
