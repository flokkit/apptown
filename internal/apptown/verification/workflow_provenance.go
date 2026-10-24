package verification

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// WorkflowProvenanceBase is the commit a run's check of tip is compared
// with, for every gate that subtracts what was already there (tests,
// scans, guidance checks): the change's fork from its default branch
// (scm.ForkBase), so after it catches up with main, main's own work is not
// the change's. tip is the commit the step checks: a landing's pinned
// candidate, else what SelectCommit read. Off a change, or without a
// repository, there is nothing to compare with (""). Git failing to answer
// is the error, never the recorded base.
func WorkflowProvenanceBase(ctx context.Context, dataDir string, source workflow.Source, tip string) (string, error) {
	if source.ChangeID.IsZero() || !source.RepoBacked() {
		return "", nil
	}
	return scm.ForkBase(ctx, source.Repository(dataDir), source.DefaultBranch, tip, source.ChangeBaseSHA)
}
