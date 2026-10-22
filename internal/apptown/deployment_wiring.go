package apptown

import (
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
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
		Authority:     authority,
		Targets:       a.targets,
		Deployments:   a.deployments,
		Hosts:         a.hosts,
		Environment:   a.environment,
		Providers:     a.targetProviders,
		Projects:      a.Projects,
		Configuration: deployment.ConfigurationDependencies{Authority: authority, Projects: sources, Contracts: sources},
		Releases:      deployment.ReleaseDependencies{Authority: authority, Sources: sources, Secrets: releaseSecrets{secrets: a.Secrets}, Queue: releaseJobs{}},
		Followups:     jobQueue{},
		Declared:      sources,
		Applied:       a.inventory.Invalidate,
	}
}

// starterDependenciesFor is what a fill from a copy runs on: Deployment's
// shared records and collaborators, built before Deployment's service (the
// need records are given by the caller).
func starterDependenciesFor(a *App) dataprotection.StarterDependencies {
	return dataprotection.StarterDependencies{Authority: access.Checked{Authority: a.Access, Projects: a.Projects}, Targets: a.targets,
		Hosts: a.hosts, Environment: a.environment, Providers: a.targetProviders,
		Catalog: a.Dependencies, Deployments: a.deployments, Sources: projectSources{projects: a.Projects, dataDir: a.cfg.dataDir},
		Secrets: a.Secrets, DataDir: a.cfg.dataDir, Now: a.cfg.clock.Now}
}

// dataProtectionDependencies are Deployment's records (the same instances
// Deployment uses), the operations ledger and restored copies' gate.
func dataProtectionDependencies(a *App) dataprotection.Dependencies {
	return dataprotection.Dependencies{Deployments: a.Deployment.Deployments, Releases: a.Deployment.Releases, Bundles: a.Deployment.Bundles,
		Retained: a.Deployment.Retained, Outcomes: a.Deployment.Outcomes, Projects: a.Projects, Integrations: a.Integrations,
		Operations: a.operations, Applied: a.inventory.Invalidate,
		RestoredCopies: dataprotection.RestoredCopyDependencies{GateImage: a.cfg.restoreGateImage, AppTown: a.cfg.publicOrigin(),
			Secrets: releaseSecrets{secrets: a.Secrets}}}
}

// hostSettings are this AppTown's Docker settings, which Deployment and
// Preview reach daemons with.
func hostSettingsFor(a *App) deployment.HostSettings {
	return deployment.HostSettings{Scope: a.docker, DataDir: a.cfg.dataDir, SSHConfigPath: a.cfg.sshConfigPath,
		BuiltInTarget: deployment.BuiltInTargetName, NoLocalDocker: a.cfg.noLocalDocker}
}

// environmentDependencies are the owners a boot's environment is prepared
// from, for a release and a preview alike.
func environmentDependenciesFor(a *App) deployment.EnvironmentDependencies {
	return deployment.EnvironmentDependencies{MemoryDefaults: a.Adoption, Secrets: a.Secrets, Integrations: a.Integrations, Needs: a.Dependencies,
		Probe: a.probe, ChownImage: check.ShImage, Routes: a.cfg.routing(), Projects: a.Projects}
}

// providerDependencies are the providers' owners and settings.
func providerDependenciesFor(a *App, digitalOcean digitalocean.Options) deployment.ProviderDependencies {
	return deployment.ProviderDependencies{Projects: a.Projects, Secrets: a.Secrets, Integrations: a.Integrations, DigitalOcean: digitalOcean,
		AfterRelease: (backupRequests{db: a.DB, life: a.life}).AfterRelease, DatabaseContainer: builtinDBFor(a.cfg.usingBuiltinDB, a.cfg.dataDir).Container, Applied: a.inventory.Invalidate, Now: a.cfg.clock.Now}
}
