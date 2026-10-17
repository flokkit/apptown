package forge

import (
	"context"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow"
)

type workflowMergeAdmission struct{ db *database.DB }

func (a workflowMergeAdmission) prepare(ctx context.Context, call workflow.MergeCall) (workflow.MergePermission, error) {
	return workflow.NewMergeAdmission(a.db, workflowLawRules{}, workflow.Placement{}).Prepare(ctx, call)
}
