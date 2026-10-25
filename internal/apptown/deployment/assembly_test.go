package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

// testRuntime is a test's Docker and provider settings and the owners they
// read, flat as a test writes them.
type testRuntime struct {
	Scope                                                                docker.Scope
	Probe                                                                *docker.Probe
	DataDir, SSHConfigPath, ChownImage, BuiltInTarget, DatabaseContainer string
	Projects                                                             *project.Projects
	Secrets                                                              *secret.Service
	Integrations                                                         *integration.Service
	Needs                                                                *dependency.Catalog
	Routes                                                               Routing
	AfterRelease                                                         func(projectID ids.ID, deployment string)
	NoLocalDocker                                                        bool
	Drivers                                                              map[string]DriverFactory
	Inventories                                                          map[string]InventoryFactory
	DigitalOcean                                                         digitalocean.Options
	S3Bucket                                                             s3bucket.Options
	Connectors                                                           ConnectorRetirement
	OfferedByConstruction                                                []string
}

// assembled is deps with the shared records and collaborators built from
// rt once, as the composition root builds them.
func assembled(db *database.DB, rt testRuntime, deps Dependencies) Dependencies {
	if rt.Projects == nil {
		// The composition root always has the project record; every
		// purpose question reads it.
		rt.Projects = project.New(db, nil)
	}
	deps.Targets = NewTargets(db, rt.Connectors, rt.OfferedByConstruction...)
	deps.Deployments = NewDeployments(db)
	deps.Projects = rt.Projects
	deps.Hosts = NewHosts(deps.Targets, rt.Secrets, HostSettings{Scope: rt.Scope, DataDir: rt.DataDir, SSHConfigPath: rt.SSHConfigPath,
		BuiltInTarget: rt.BuiltInTarget, NoLocalDocker: rt.NoLocalDocker})
	deps.Environment = NewEnvironment(EnvironmentDependencies{Secrets: rt.Secrets, Integrations: rt.Integrations, Needs: rt.Needs,
		Probe: rt.Probe, ToolVersions: chownTools(rt.ChownImage), Routes: rt.Routes, Projects: rt.Projects})
	deps.Providers = NewProviders(context.Background(), db, ProviderRecords{Targets: deps.Targets, Deployments: deps.Deployments}, deps.Hosts, deps.Environment,
		ProviderDependencies{Projects: rt.Projects, Secrets: rt.Secrets, Integrations: rt.Integrations, DigitalOcean: rt.DigitalOcean, S3Bucket: rt.S3Bucket,
			Drivers: rt.Drivers, Inventories: rt.Inventories, AfterRelease: rt.AfterRelease, DatabaseContainer: rt.DatabaseContainer, Applied: deps.Applied, Now: deps.Now})
	return deps
}

// chownTools is a test runtime's tool versions: the stock list's images,
// with chown as the runtime names it ("" skips handing volumes over).
func chownTools(chown string) toolversions.Source {
	s := stock.Versions().Current()
	s.Images.Sh = chown
	return fixedTools{s}
}

type fixedTools struct{ s toolversions.Snapshot }

func (f fixedTools) Current() toolversions.Snapshot { return f.s }
