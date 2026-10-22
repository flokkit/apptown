package apptown

import (
	"github.com/flokkit/apptown/internal/apptown/mcpapi"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
)

func mcpToolsFor(a *App) mcpapi.Catalog {
	scope := mcpapi.NewScope(a.Projects, a.Access)
	return mcpapi.Assemble(
		mcpapi.NewProjectReads(mcpapi.ProjectReadsDependencies{Scope: scope, Work: a.Work, Adoption: a.Adoption, Ways: a.Ways, Conformance: a.conformance, Deployments: a.Deployment, DB: a.DB, DataDir: a.cfg.dataDir}),
		mcpapi.NewWorkReads(mcpapi.WorkReadsDependencies{Scope: scope, Work: a.Work, DB: a.DB, Contributions: contributionsFor(a)}),
		mcpapi.NewLaneCommands(mcpapi.LaneCommandsDependencies{Scope: scope, Work: a.Work, Adoption: a.Adoption, DB: a.DB, Now: a.cfg.clock.Now}),
		mcpapi.NewWorkCommands(mcpapi.WorkCommandsDependencies{Scope: scope, Work: a.Work, Actions: a.changeActions, DB: a.DB}),
		mcpapi.NewChecks(mcpapi.ChecksDependencies{Scope: scope, Work: a.Work, Verification: a.verification}),
		mcpapi.NewPreviews(mcpapi.PreviewsDependencies{Scope: scope, Work: a.Work, Previews: a.Preview, Preview: changePreviewViewsFor(a), DB: a.DB, Now: a.cfg.clock.Now}),
		mcpapi.NewComplaints(mcpapi.ComplaintsDependencies{Scope: scope, Work: a.Work, Complaints: a.Complaints}),
		mcpapi.NewProjectCreation(mcpapi.ProjectCreationDependencies{Scope: scope, Lifecycle: a.lifecycle, DB: a.DB, Now: a.cfg.clock.Now}),
		mcpapi.NewContributions(mcpapi.ContributionsDependencies{Scope: scope, Work: a.Work, Contributions: contributionsFor(a), Adoption: a.Adoption}),
		mcpapi.NewWhatIfReads(mcpapi.WhatIfReadsDependencies{Conformance: a.conformance}),
		mcpapi.NewEvalReads(mcpapi.EvalReadsDependencies{Evals: a.Evals}),
		mcpapi.NewOperationReads(mcpapi.OperationReadsDependencies{Operations: a.operations}),
	)
}
func contributionsFor(a *App) *repositoryedit.Contributions {
	return repositoryedit.NewContributions(a.DB, a.Work, a.Access, a.Projects, a.branches, a.cfg.dataDir, holderPresenceFor(a), &a.repositories)
}
