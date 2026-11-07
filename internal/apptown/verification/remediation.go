package verification

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ReviewRemediation evaluates only the item and observation supplied by its owner.
// --- the review ---------------------------------------------------------------------

func (f Fleet) ReviewRemediation(ctx context.Context, item remediation.Item, scope remediation.VerificationScope) remediation.Review {
	ctx = conformance.WithVerification(ctx, scope)
	landing, err := database.Transact(ctx, f.db, func(tx *database.Tx) (*workflow.ExecutionObservation, error) {
		return workflow.ObserveExecutionIn(ctx, tx, workflow.ExecutionSelection{Change: workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID}, LatestLanding: true})
	})
	if err != nil {
		return remediation.Review{State: conformance.EvaluationError, Detail: err.Error()}
	}
	if landing != nil {
		v, err := f.checks.LandingVerdict(ctx, item.ProjectID, landing.Run.ID, conformance.CheckIdentity{Block: item.Block, Version: item.Version, Check: item.Check})
		if err != nil {
			return remediation.Review{State: conformance.EvaluationError, Detail: err.Error()}
		}
		if v != nil {
			return remediation.Review{ID: v.ReviewID, State: v.State, Detail: v.Detail}
		}
	}
	// Verification may build so exec-image tool checks can be judged too.
	rev, verdicts, err := f.RunFleetReviewWith(ctx, "remediation", conformance.ReviewOptions{Projects: []ids.ID{item.ProjectID}, Build: true, BuildBudget: 1})
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
