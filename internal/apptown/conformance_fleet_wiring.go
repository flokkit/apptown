package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func fleetReviewsFor(a *App) *verification.Fleet {
	return verification.NewFleet(verification.FleetDependencies{Checks: a.conformance, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now, a.cfg.stock.Tools), Scanners: a.scanners, Selection: a.modelSelection, Ledger: a.Usage.Ledger, DB: a.DB, Reporting: selfErrorsFor(a), DataDir: a.cfg.dataDir, Candidates: a.Adoption, Runtime: runtimeFor(a), Jobs: jobQueue{}})
}

func openRemediationsFor(db *database.DB) conformance.OpenRemediations {
	return func(ctx context.Context, projects []ids.ID) ([]conformance.RemediationCoverage, error) {
		covered, err := workflow.OpenRemediationsIn(ctx, db, projects)
		if err != nil {
			return nil, err
		}
		out := make([]conformance.RemediationCoverage, 0, len(covered))
		for _, c := range covered {
			out = append(out, conformance.RemediationCoverage{ProjectID: c.ProjectID, ChangeID: c.ChangeID, Number: c.Number, Block: c.Block, Version: c.Version, Check: c.Check})
		}
		return out, nil
	}
}
