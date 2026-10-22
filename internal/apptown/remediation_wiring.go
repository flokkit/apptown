package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
)

// What the App supplies remediation (architecture.md §6): Work's doors under
// AppTown principal, the conformance lens, the resolver that turns an item
// into a frozen start definition, the post-landing review, and whether a
// person is in the workshop on the project.

func (a *App) remediationDependencies() remediation.Dependencies {
	starts := workcommands.RemediationStarts{DB: a.DB, Work: a.Work, Preparation: a.preparation, Adoption: a.Adoption, Lanes: a.lifecycle, Checks: a.conformance, DataDir: a.cfg.dataDir}
	sessions := a.Console

	return remediation.Dependencies{
		Now:     a.cfg.clock.Now,
		Work:    a.Work,
		Caller:  a.asAppTown(a.life, ids.Nil, "remediation"),
		Checks:  a.conformance,
		Resolve: starts.Resolve,
		Review:  fleetReviewsFor(a).ReviewRemediation,
		Busy: func(ctx context.Context, projectID ids.ID) bool {
			return sessions.Busy(ctx, conversation.ForProject(projectID), remediation.BusyConsoleWindow)
		},
		RequestPass: passRequests{db: a.DB, kind: remediation.PassJob}.Request,
	}
}
