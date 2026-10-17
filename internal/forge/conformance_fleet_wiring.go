package forge

import (
	"flokkit.io/forge/internal/forge/verification"
)

func fleetReviewsFor(a *App) *verification.Fleet {
	return verification.NewFleet(verification.FleetDependencies{Checks: a.conformance, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), Scanners: a.scanners, Selection: a.modelSelection, Ledger: a.Usage.Ledger, DB: a.DB, Reporting: selfErrorsFor(a), DataDir: a.cfg.dataDir, Candidates: a.Adoption, Runtime: runtimeFor(a), Jobs: jobQueue{}})
}
