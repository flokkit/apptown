package apptown

import (
	"github.com/flokkit/apptown/internal/apptown/verification"
)

func fleetReviewsFor(a *App) *verification.Fleet {
	return verification.NewFleet(verification.FleetDependencies{Checks: a.conformance, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now, a.cfg.stock.Tools), Scanners: a.scanners, Selection: a.modelSelection, Ledger: a.Usage.Ledger, DB: a.DB, Reporting: selfErrorsFor(a), DataDir: a.cfg.dataDir, Candidates: a.Adoption, Runtime: runtimeFor(a), Jobs: jobQueue{}})
}
