package dataprotection

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

// The package's rules are tested against the canonical schema, each test
// on its own clone of one template database (internal/dbtest).
var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) {
	code := databaseSuite.Run(m)
	gittest.RemoveTemplates()
	os.Exit(code)
}

func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func mustExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func createTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	return createProjectFor(t, db, slug, name, project.Ordinary)
}

func createProjectFor(t *testing.T, db *database.DB, slug, name string, purpose project.Purpose) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name, Purpose: purpose})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// both is Deployment and data protection over the same records and
// collaborators, as a test drives them: a placement is bound and released
// through Deployment, and its data copied, restored and taken down through
// data protection.
type both struct {
	*Service
	*deploymentOwner
}

// deploymentOwner names Deployment's service for embedding beside data
// protection's, whose type has the same name.
type deploymentOwner = deployment.Service

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
	Routes                                                               deployment.Routing
	AfterRelease                                                         func(projectID ids.ID, deployment string)
	NoLocalDocker                                                        bool
	Drivers                                                              map[string]deployment.DriverFactory
	Inventories                                                          map[string]deployment.InventoryFactory
	DigitalOcean                                                         digitalocean.Options
	S3Bucket                                                             s3bucket.Options
	Connectors                                                           deployment.ConnectorRetirement
	OfferedByConstruction                                                []string
	RestoredCopies                                                       RestoredCopyDependencies
}

// assembled is Deployment and data protection built over the shared
// records and collaborators from rt once, as the composition root builds
// them: the Starter first, then Deployment given it, then data protection
// over the same Starter.
func assembled(db *database.DB, rt testRuntime, deps deployment.Dependencies) *both {
	ctx := context.Background()
	if rt.Projects == nil {
		// The composition root always has the project record; every
		// purpose question reads it.
		rt.Projects = project.New(db, nil)
	}
	deps.Targets = deployment.NewTargets(db, rt.Connectors, rt.OfferedByConstruction...)
	deps.Deployments = deployment.NewDeployments(db)
	deps.Projects = rt.Projects
	deps.Hosts = deployment.NewHosts(deps.Targets, rt.Secrets, deployment.HostSettings{Scope: rt.Scope, DataDir: rt.DataDir, SSHConfigPath: rt.SSHConfigPath,
		BuiltInTarget: rt.BuiltInTarget, NoLocalDocker: rt.NoLocalDocker})
	deps.Environment = deployment.NewEnvironment(deployment.EnvironmentDependencies{Secrets: rt.Secrets, Integrations: rt.Integrations, Needs: rt.Needs,
		Probe: rt.Probe, ToolVersions: chownTools(rt.ChownImage), Routes: rt.Routes, Projects: rt.Projects})
	deps.Providers = deployment.NewProviders(ctx, db, deployment.ProviderRecords{Targets: deps.Targets, Deployments: deps.Deployments}, deps.Hosts, deps.Environment,
		deployment.ProviderDependencies{Projects: rt.Projects, Secrets: rt.Secrets, Integrations: rt.Integrations, DigitalOcean: rt.DigitalOcean, S3Bucket: rt.S3Bucket,
			Drivers: rt.Drivers, Inventories: rt.Inventories, AfterRelease: rt.AfterRelease, DatabaseContainer: rt.DatabaseContainer, Applied: deps.Applied, Now: deps.Now})
	starter := NewStarter(db, StarterDependencies{Authority: deps.Authority, Still: deps.Releases.Still, Projects: rt.Projects, Targets: deps.Targets, Hosts: deps.Hosts,
		Environment: deps.Environment, Providers: deps.Providers, Catalog: rt.Needs, Deployments: deps.Deployments, Sources: deps.Releases.Sources, Secrets: rt.Secrets, DataDir: rt.DataDir, Now: deps.Now})
	deps.CopyStarter = starter
	deps.CopyCheck = starter
	d := deployment.New(ctx, db, deps)
	s := New(starter, Dependencies{Deployments: d.Deployments, Releases: d.Releases, Bundles: d.Bundles, Retained: d.Retained, Outcomes: d.Outcomes,
		Projects: rt.Projects, Integrations: rt.Integrations, Operations: deps.Operations, RestoredCopies: rt.RestoredCopies,
		Applied: deps.Applied})
	return &both{Service: s, deploymentOwner: d}
}

// setClock puts data protection on AppTown's clock now.
func (b *both) setClock(now func() time.Time) {
	b.Service.clock, b.Service.backups.clock = now, now
}

var errAttemptLost = errors.New("the attempt does not own its step any more")

// chownTools is a test runtime's tool versions: the stock list's images,
// with chown as the runtime names it ("" skips handing volumes over).
func chownTools(chown string) toolversions.Source {
	s := stock.Versions().Current()
	s.Images.Sh = chown
	return fixedTools{s}
}

type fixedTools struct{ s toolversions.Snapshot }

func (f fixedTools) Current() toolversions.Snapshot { return f.s }
