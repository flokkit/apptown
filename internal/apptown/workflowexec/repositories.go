package workflowexec

import (
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

const MergeRequestKind = "scm/mr"
const ReconcileKind = "reconcile"

// repositories adapts repository effects to admitted workflow steps. The
// provider resolves credentials at use; no transport or App reaches the runner.
type repositories struct {
	db          *database.DB
	attachments *scm.RepositoryAttachments
	branches    *scm.Branches
	cfg         RepositoryConfig
	clock       func() time.Time // AppTown's clock; nil is the real one
}
type RepositoryConfig struct {
	DataDir, PublicHost string
	// Now is AppTown's clock, which stamps what a merge request records
	// on a run; nil is the real one.
	Now func() time.Time
}

func NewRepositories(db *database.DB, attachments *scm.RepositoryAttachments, branches *scm.Branches, cfg RepositoryConfig) *repositories {
	return &repositories{db: db, attachments: attachments, branches: branches, cfg: cfg, clock: cfg.Now}
}

// now reads AppTown's clock.
func (a *repositories) now() time.Time { return clock.Or(a.clock)() }
