package verification

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/ids"
)

// ReviewRemediation evaluates only the item and observation supplied by its owner.
// --- the review ---------------------------------------------------------------------

func (f Fleet) ReviewRemediation(ctx context.Context, item remediation.Item, scope remediation.VerificationScope) remediation.Review {
	// Verification may build so exec-image tool checks can be judged too.
	rev, verdicts, err := f.RunFleetReviewWith(conformance.WithVerification(ctx, scope), "remediation", conformance.ReviewOptions{Projects: []ids.ID{item.ProjectID}, Build: true, BuildBudget: 1})
	if err != nil {
		return remediation.Review{State: conformance.EvaluationError, Detail: err.Error()}
	}
	for _, verdict := range verdicts {
		if verdict.Block == item.Block && verdict.Check == item.Check {
			return remediation.Review{ID: rev.ID, State: verdict.State, Detail: verdict.Detail}
		}
	}
	return remediation.Review{ID: rev.ID, State: conformance.NotScanned, Detail: "the project no longer adopts this check"}
}
