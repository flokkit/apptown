package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/ids"
)

// Composition: what Deployment asks of the world, supplied by AppTown once
// at construction (architecture.md §5.6, §7). Drivers receive their own
// operations; neither App nor a raw database is an escape hatch.
func (a *App) deploymentDependencies(changes workflow.Changes) deployment.Dependencies {
	authority := access.Checked{Authority: a.Access, Projects: a.Projects}
	sources := projectSources{projects: a.Projects, dataDir: a.cfg.dataDir}
	return deployment.Dependencies{
		// AppTown's one clock, the one project deletion reads too: an
		// orphaned copy's age is its taking against the deletion's.
		Now:           a.cfg.clock.Now,
		Landings:      landingDeployReads{changes},
		Authority:     authority,
		Groups:        a.Access,
		Targets:       a.targets,
		Deployments:   a.deployments,
		Hosts:         a.hosts,
		Environment:   a.environment,
		Providers:     a.targetProviders,
		Projects:      a.Projects,
		Configuration: deployment.ConfigurationDependencies{Authority: authority, Projects: sources, Contracts: sources},
		Releases:      deployment.ReleaseDependencies{Authority: authority, Still: a.Access, Sources: sources, Secrets: releaseSecrets{secrets: a.Secrets}, Queue: releaseJobs{}},
		Followups:     jobQueue{},
		Declared:      sources,
		Applied:       a.infrastructure.Applied,
	}
}

// starterDependenciesFor is what a fill from a copy runs on: Deployment's
// shared records and collaborators, built before Deployment's service (the
// need records are given by the caller).
func starterDependenciesFor(a *App) dataprotection.StarterDependencies {
	return dataprotection.StarterDependencies{Authority: access.Checked{Authority: a.Access, Projects: a.Projects}, Still: a.Access, Projects: a.Projects, Targets: a.targets,
		Hosts: a.hosts, Environment: a.environment, Providers: a.targetProviders,
		Catalog: a.Dependencies, Deployments: a.deployments, Sources: projectSources{projects: a.Projects, dataDir: a.cfg.dataDir},
		Secrets: a.Secrets, DataDir: a.cfg.dataDir, Now: a.cfg.clock.Now}
}

// dataProtectionDependencies are Deployment's records (the same instances
// Deployment uses), the operations ledger and restored copies' gate.
func dataProtectionDependencies(a *App) dataprotection.Dependencies {
	return dataprotection.Dependencies{Deployments: a.Deployment.Deployments, Releases: a.Deployment.Releases, Bundles: a.Deployment.Bundles,
		Retained: a.Deployment.Retained, Outcomes: a.Deployment.Outcomes, Projects: a.Projects, Integrations: a.Integrations,
		Operations: a.operations, Applied: a.infrastructure.Applied,
		RestoredCopies: dataprotection.RestoredCopyDependencies{GateImage: a.cfg.restoreGateImage, AppTown: a.cfg.publicOrigin(),
			Secrets: releaseSecrets{secrets: a.Secrets}}}
}

// instanceBackupDependencies are what a whole-AppTown backup and the
// record of a restore run on: the database it dumps (the built-in one or
// the -db-url one, in a pg_dump container), data protection, Deployment's records, secrets and the hold of new work.
func instanceBackupDependencies(a *App) instancebackup.Dependencies {
	deps := instancebackup.Dependencies{DB: a.DB, DataDir: a.cfg.dataDir, Protection: a.DataProtection, Deployment: a.Deployment,
		Deployments: a.Deployment.Deployments, Targets: a.targets, BuiltInTarget: a.hosts.BuiltInTarget(), Secrets: a.Secrets,
		NewWork: &a.newWork, Now: a.cfg.clock.Now}
	deps.Database = databaseDump{DB: a.DB, Place: &a.dumps, Tools: a.cfg.stock.Tools, DataDir: a.cfg.dataDir}
	if !a.cfg.usingBuiltinDB {
		deps.Database = databaseDump{DB: a.DB, Place: &a.dumps, Tools: a.cfg.stock.Tools, URL: a.cfg.dbURL}
	}
	return deps
}

// hostSettings are this AppTown's Docker settings, which Deployment and
// Preview reach daemons with.
func hostSettingsFor(a *App) deployment.HostSettings {
	return deployment.HostSettings{Scope: a.docker, DataDir: a.cfg.dataDir, SSHConfigPath: a.cfg.sshConfigPath, SSHCommand: a.cfg.targetSSHCommand,
		BuiltInTarget: deployment.BuiltInTargetName, NoLocalDocker: a.cfg.noLocalDocker}
}

// environmentDependencies are the owners a boot's environment is prepared
// from, for a release and a preview alike.
func environmentDependenciesFor(a *App) deployment.EnvironmentDependencies {
	return deployment.EnvironmentDependencies{Secrets: a.Secrets, Integrations: a.Integrations, Needs: a.Dependencies,
		Probe: a.probe, ToolVersions: a.cfg.stock.Tools, Routes: a.cfg.routing(), Projects: a.Projects}
}

// providerDependencies are the providers' owners and settings.
func providerDependenciesFor(a *App, digitalOcean digitalocean.Options) deployment.ProviderDependencies {
	return deployment.ProviderDependencies{Projects: a.Projects, Secrets: a.Secrets, Integrations: a.Integrations, DigitalOcean: digitalOcean,
		AfterRelease: (backupRequests{db: a.DB, life: a.life}).AfterRelease, DatabaseContainer: builtinDBFor(a.cfg.usingBuiltinDB, a.cfg.dataDir).Container, Applied: a.infrastructure.Applied, Now: a.cfg.clock.Now, Installation: a.docker.Instance}
}

// Work retains invocation outcomes; Deployment selects the named placement.
type landingDeployReads struct{ changes workflow.Changes }

func (r landingDeployReads) LandingDeployAttempts(ctx context.Context, project ids.ID) ([]deployment.LandingDeployAttempt, error) {
	attempts, err := r.changes.LandingDeployAttempts(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]deployment.LandingDeployAttempt, 0, len(attempts))
	for _, a := range attempts {
		step := a.Step
		out = append(out, deployment.LandingDeployAttempt{OutcomeKnown: a.OutcomeKnown, Attempt: a.Attempt, ChangeID: a.ChangeID, ChangeNumber: a.ChangeNumber, ChangeTitle: a.ChangeTitle, RunStartedAt: a.RunStartedAt,
			Step: deployment.LandingDeployStep{ID: step.ID, RunID: step.RunID, Type: step.Type, Status: step.Status.String(), Config: step.Config, Evidence: step.Evidence, StartedAt: step.StartedAt, FinishedAt: step.FinishedAt}})
	}
	return out, nil
}
