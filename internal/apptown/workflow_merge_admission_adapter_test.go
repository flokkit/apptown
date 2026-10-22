package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type workflowMergeAdmission struct{ db *database.DB }

func (a workflowMergeAdmission) prepare(ctx context.Context, call workflow.MergeCall) (workflow.MergePermission, error) {
	return workflow.NewMergeAdmission(a.db, workflowLawRules{}, workflow.Placement{}).Prepare(ctx, call)
}
