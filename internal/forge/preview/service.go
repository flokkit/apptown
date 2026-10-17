package preview

import (
	"sync"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
)

// Service owns previews: the checked requests for a change's and a
// release's preview, the trusted boots of a run's preview and a chat's
// rehearsal, where each boots (this machine or the worker), what each
// serves, opening a running one for probing, discarding them, clearing the
// worker's when the forge starts, and the AI models keys previews are
// given. One instance holds the boot locks, the served records and the
// discard counts; nothing of it survives a restart except the keys'
// records, so every key is revoked once its preview is gone, and the last
// look at each preview, which decides when it stops and how long its build
// is kept.
type Service struct {
	db           *database.DB
	authority    access.Authorizer
	sources      Sources
	host         Host
	releaseHost  ReleaseHost
	worker       Worker
	hosts        *deployment.Hosts
	env          *deployment.Environment
	targets      *deployment.Targets
	deployments  *deployment.Deployments
	releases     *deployment.Releases
	bundles      *deployment.Bundles
	retained     *deployment.Retained
	projects     *project.Projects
	integrations *integration.Service
	dataDir      string
	clock        func() time.Time // the forge's clock; nil is the real one

	runtime    Runtime        // boot serialization and transient status, by preview
	served     servedPreviews // what each preview this forge booted serves
	booting    sync.Map       // name → *sync.Mutex: a preview booting (holdPreview)
	startClear startClear     // the worker's previews from before this start, until removed
	looks      looks          // when each preview's last look was written
}

// Dependencies are what previews read and run on. Deployment's records and
// collaborators are the same instances Deployment uses; Preview writes only
// its own table and Deployment's records through their operations.
type Dependencies struct {
	Authority access.Authorizer
	// Sources resolves a change and a console session, and pins a change a
	// chat on the worker holds.
	Sources Sources
	// Worker boots changes' and runs' previews when the forge has a worker;
	// nil boots them on this machine.
	Worker      Worker
	Hosts       *deployment.Hosts
	Environment *deployment.Environment
	// Targets names the built-in host a change preview's placement row is
	// on; Deployments keeps that row; Releases and Bundles are what a
	// release's preview boots; Retained keeps a key whose revocation failed.
	Targets      *deployment.Targets
	Deployments  *deployment.Deployments
	Releases     *deployment.Releases
	Bundles      *deployment.Bundles
	Retained     *deployment.Retained
	Projects     *project.Projects
	Integrations *integration.Service
	// DataDir is the forge's data directory: changes' folders and the
	// snapshots a preview serves are under it.
	DataDir string
	// Now is the forge's clock; nil is the real one.
	Now func() time.Time
	// Host and ReleaseHost replace the boots of a change's and a release's
	// preview (a test's); nil boots them here.
	Host        Host
	ReleaseHost ReleaseHost
}

// New builds the preview owner.
func New(db *database.DB, deps Dependencies) *Service {
	s := &Service{db: db, authority: deps.Authority, sources: deps.Sources, host: deps.Host, releaseHost: deps.ReleaseHost, worker: deps.Worker,
		hosts: deps.Hosts, env: deps.Environment, targets: deps.Targets, deployments: deps.Deployments, releases: deps.Releases,
		bundles: deps.Bundles, retained: deps.Retained, projects: deps.Projects, integrations: deps.Integrations, dataDir: deps.DataDir,
		clock: deps.Now}
	s.runtime.Now, s.startClear.every = deps.Now, time.Minute
	if s.host == nil {
		s.host = changeBoots{s}
	}
	if s.releaseHost == nil {
		s.releaseHost = releaseBoots{s}
	}
	return s
}

// now reads the forge's clock.
func (s *Service) now() time.Time { return clock.Or(s.clock)() }
