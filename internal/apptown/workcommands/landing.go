package workcommands

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// LandingEffects supplies a per-operation driver and records observed effects.
// Workflow retains admission/settlement; SCM retains the external merge protocol.
type LandingEffects struct {
	DB        *database.DB
	Contracts *project.Contracts
	DataDir   string
	Drivers   scm.GitDrivers
	Publisher repositoryedit.Publisher
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
		if result.Source.MR.IID == 0 {
			m.Publisher.PushBranch(ctx, result.Source.ProjectID, result.Source.Repository, result.Source.DefaultBranch)
		}
	}
}
