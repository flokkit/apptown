package verification

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
)

type reporter interface {
	Report(kind, where, message string, tags map[string]string, stack string)
}
type Fleet struct {
	checks    *conformance.Service
	evaluator *evaluator
	scanners  *Scanners
	selection model.Selection
	ledger    *usage.Ledger
	db        *database.DB
	reporting reporter
	dataDir   string
	// candidates prepares what a project would adopt, for a what-if.
	candidates candidatePreparer
	// runtime observes deployments: a what-if probes the app serving the
	// commit it reads. Nil where AppTown observes none.
	runtime *Runtime
	// jobs queues the jobs that run admitted reviews and what-ifs.
	jobs jobs.Enqueuer
}

type FleetDependencies struct {
	Checks    *conformance.Service
	Evaluator *evaluator
	Scanners  *Scanners
	Selection model.Selection
	Ledger    *usage.Ledger
	DB        *database.DB
	Reporting reporter
	DataDir   string
	// Candidates is Adoption's candidate preparation, for what-ifs.
	Candidates candidatePreparer
	// Runtime observes deployments, for what-ifs' runtime probes.
	Runtime *Runtime
	// Jobs queues the jobs that run admitted reviews and what-ifs.
	Jobs jobs.Enqueuer
}

func NewFleet(d FleetDependencies) *Fleet {
	return &Fleet{checks: d.Checks, evaluator: d.Evaluator, scanners: d.Scanners, selection: d.Selection, ledger: d.Ledger, db: d.DB, reporting: d.Reporting, dataDir: d.DataDir, candidates: d.Candidates, runtime: d.Runtime, jobs: d.Jobs}
}

// judgeChecklist asks call with the fast model of the project it is for.
func (f Fleet) judgeChecklist(ctx context.Context, call modelcall.Call, items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	var err error
	if call.Model, _, _, err = f.selection.Resolve(ctx, call.ProjectID, "fast"); err != nil {
		return nil, nil, err
	}
	return f.evaluator.checklist(ctx, call, items, diff)
}
