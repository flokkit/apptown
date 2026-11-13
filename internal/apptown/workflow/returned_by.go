package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ReturnedBy describes the latest return to work, until a later landing or
// agent round starts. It is independent of a currently reworking agent.
type ReturnedBy struct {
	By           string `json:"by"`
	At           string `json:"at"`
	Note         string `json:"note"`
	RejectedStep ids.ID `json:"rejectedStep,omitzero"`
}

func returnedByIn(ctx context.Context, db database.Queryer, ch *Change) (*ReturnedBy, error) {
	if ch.State != ChangeInProgress {
		return nil, nil
	}
	receipts, err := audit.Recent(ctx, db, audit.Filter{ProjectID: ch.ProjectID, KindPrefix: "change.sendback", SubjectFamily: fmt.Sprintf("change %d", ch.ID), Limit: 1})
	if err != nil || len(receipts) == 0 {
		return nil, err
	}
	receipt := receipts[0]
	var payload sendBackReceipt
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return nil, err
	}
	later, err := workflowsql.New(db.SQL()).WorkStartedAfterReturn(ctx, workflowsql.WorkStartedAfterReturnParams{ProjectID: ch.ProjectID, ChangeID: ch.ID, ReceiptID: receipt.ID})
	if err != nil || later {
		return nil, err
	}

	return &ReturnedBy{By: receipt.Actor, At: receipt.At, Note: payload.Note, RejectedStep: payload.RejectedStep}, nil
}
