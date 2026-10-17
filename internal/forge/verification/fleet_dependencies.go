package verification

import (
	"context"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/modelcall"
	"flokkit.io/forge/internal/forge/usage"
)

type Reporter interface {
	Report(kind, where, message string, tags map[string]string, stack string)
}
type Fleet struct {
	checks    *conformance.Service
	evaluator *Evaluator
	scanners  *Scanners
	selection model.Selection
	ledger    *usage.Ledger
	db        *database.DB
	reporting Reporter
	dataDir   string
	// candidates prepares what a project would adopt, for a what-if.
	candidates CandidatePreparer
	// runtime observes deployments: a what-if probes the app serving the
	// commit it reads. Nil where the forge observes none.
	runtime *Runtime
	// jobs queues the jobs that run admitted reviews and what-ifs.
	jobs jobs.Enqueuer
}

type FleetDependencies struct {
	Checks    *conformance.Service
	Evaluator *Evaluator
	Scanners  *Scanners
	Selection model.Selection
	Ledger    *usage.Ledger
	DB        *database.DB
	Reporting Reporter
	DataDir   string
	// Candidates is Adoption's candidate preparation, for what-ifs.
	Candidates CandidatePreparer
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
	call.Model, _, _ = f.selection.Resolve(ctx, call.ProjectID, "fast")
	return f.evaluator.Checklist(ctx, call, items, diff)
}
