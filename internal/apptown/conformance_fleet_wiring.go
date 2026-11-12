package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func fleetReviewsFor(a *App) *verification.Fleet {
	return verification.NewFleet(verification.FleetDependencies{Standing: &a.standingRules, Publications: packPublicationFeed{}, PackAdoptions: workcommands.PackAdoptions{Adoption: a.Adoption, Work: a.Work, Rules: &a.standingRules}, Checks: a.conformance, Evaluator: checkEvaluatorFor(a), Scanners: a.scanners, Selection: a.modelSelection, Ledger: a.Usage.Ledger, DB: a.DB, Reporting: selfErrorsFor(a), DataDir: a.cfg.dataDir, Candidates: a.Adoption, Runtime: runtimeFor(a), Jobs: jobQueue{}})
}

func openRemediationsFor(db *database.DB) conformance.OpenRemediations {
	return func(ctx context.Context, projects []ids.ID) ([]conformance.RemediationCoverage, error) {
		covered, err := remediation.CoverageIn(ctx, db, projects)
		if err != nil {
			return nil, err
		}
		out := make([]conformance.RemediationCoverage, 0, len(covered))
		for _, c := range covered {
			out = append(out, conformance.RemediationCoverage{ProjectID: c.ProjectID, ChangeID: c.ChangeID, Number: c.ChangeNumber, RemediationID: c.RemediationID, RemediationNumber: c.RemediationNumber, Block: c.Block, Version: c.Version, Check: c.Check})
		}
		return out, nil
	}
}

// Publication adapters bind the late-constructed services without owning policy.
type packPublicationReviews struct{ fleet func() *verification.Fleet }

func (p packPublicationReviews) PublishedPackIn(ctx context.Context, tx *database.Tx, name string, version int64) error {
	return p.fleet().PublishedPackIn(ctx, tx, name, version)
}
func (p packPublicationReviews) PackRuleEnabledIn(ctx context.Context, tx *database.Tx, rule workflow.StandingRevision) error {
	return p.fleet().PackRuleEnabledIn(ctx, tx, rule)
}

type packPublicationFeed struct{}

func (packPublicationFeed) LatestPackPublicationsIn(ctx context.Context, q database.Queryer) ([]verification.PackPublication, error) {
	packs, err := wow.PacksIn(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]verification.PackPublication, 0, len(packs))
	for _, p := range packs {
		out = append(out, verification.PackPublication{Name: p.Name, Version: p.LatestVersion})
	}
	return out, nil
}
