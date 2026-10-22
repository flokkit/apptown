package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// ADR edits publish committed evidence to Work without retaining App.
func refreshADREvidence(work *workflow.Service, caller context.Context) func(adrs.Record) {
	return func(record adrs.Record) {
		if !record.ChangeID.IsZero() {
			if err := work.RefreshChangeEvidence(caller, record.ProjectID, record.ChangeID); err != nil {
				reportWorkflowDispatch(err)
			}
		}
	}
}
