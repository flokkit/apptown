package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type workspacePreparedReceipt struct {
	Branch  string `json:"branch"`
	Tip     string `json:"tip"`
	Created bool   `json:"created"`
}

func recordWorkspaceEventIn(ctx context.Context, tx *database.Tx, runID ids.ID, payload workspacePreparedReceipt, at time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	n, err := workflowsql.New(tx.SQL()).InsertWorkspacePreparedEvent(ctx, workflowsql.InsertWorkspacePreparedEventParams{ID: ids.New(at), RunID: runID, At: at, PayloadJson: encoded})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("workspace preparation receipt was not recorded")
	}
	return nil
}
