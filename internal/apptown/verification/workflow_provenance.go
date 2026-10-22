package verification

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Scans compare against the change's fork, not the tip recorded when a landing
// began. The checked source carries the recorded fallback and repository identity.
func WorkflowProvenanceBase(ctx context.Context, dataDir string, source workflow.Source) string {
	if source.ChangeID.IsZero() {
		return ""
	}
	return strs.FirstNonBlank(workflowForkPoint(ctx, dataDir, source), strings.TrimSpace(source.ChangeBaseSHA))
}

func workflowForkPoint(ctx context.Context, dataDir string, source workflow.Source) string {
	if !source.RepoBacked() {
		return ""
	}
	out, err := gitexec.Run(ctx, source.Repository(dataDir), "merge-base", source.DefaultBranch, source.Branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
