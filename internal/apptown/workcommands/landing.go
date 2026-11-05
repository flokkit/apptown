package workcommands

import (
	"context"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// LandingEffects supplies a per-operation driver and records observed effects.
// Workflow retains admission/settlement; SCM retains the external merge protocol.
// A failure to record conformance is logged; the completed landing stands.
type LandingEffects struct {
	DB          *database.DB
	Contracts   *project.Contracts
	Conformance *conformance.Service
	DataDir     string
	Drivers     scm.GitDrivers
	Publisher   repositoryedit.Publisher
	// Now is AppTown's clock, which stamps what a landing observed (the
	// real clock when nil).
	Now func() time.Time
}

func (m LandingEffects) Driver() scm.Driver { return m.Drivers.Driver() }
func (m LandingEffects) Observed(ctx context.Context, result scm.Result) {
	if len(result.Drift) > 0 {
		audit.At(clock.Or(m.Now)()).BestEffort(ctx, m.DB, "apptown-system", result.Source.ProjectID, "integrity.out-of-band-edits", "detected", struct {
			Files []string `json:"files"`
			Diff  string   `json:"diff"`
		}{result.Drift, result.DriftDiff})
	}
	if result.Merged && result.SHA != "" {
		m.Contracts.RecordCommit(ctx, m.DataDir, result.Source.ProjectID, result.SHA)
		if err := m.recordLandingResults(ctx, result); err != nil {
			log.Printf("recording landing conformance for run %s: %v", result.Source.RunID, err)
		}
		if result.Source.MR.IID == 0 {
			m.Publisher.PushBranch(ctx, result.Source.ProjectID, result.Source.Repository, result.Source.DefaultBranch)
		}
	}
}

func (m LandingEffects) recordLandingResults(ctx context.Context, result scm.Result) error {
	steps, err := workflow.ReadScopedStepsIn(ctx, m.DB, workflow.RunRef{ID: result.Source.RunID, ProjectID: result.Source.ProjectID})
	if err != nil {
		return err
	}
	var receipts []conformance.LandingReceipt
	found := false
	for _, step := range steps {
		if step.Type != "guidance/conformance" {
			continue
		}
		found = true
		evidence, err := workflow.ReadCheckEvidence(step.Evidence)
		if err != nil {
			return err
		}
		for _, r := range evidence.Checks {
			receipts = append(receipts, conformance.LandingReceipt{AgentEvidence: r.AgentEvidence,
				Block: r.Block, Version: r.Version, Check: r.Check, Verdict: r.Verdict.String(),
				EvaluatorVer: r.EvaluatorVer, EvidenceHash: r.EvidenceHash,
				Detail: r.Detail, Output: r.Output, Note: r.Note, ExceptedBy: r.ExceptedBy,
			})
		}
	}
	if !found {
		return nil
	}
	return m.Conformance.RecordLandingResults(ctx, result.Source.ProjectID, result.SHA, result.Source.RunID, result.Source.Candidate, receipts)
}
