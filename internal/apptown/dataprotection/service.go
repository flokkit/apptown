package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// core is what copies, fills and the backup store run on: the deployment
// records and collaborators that exist before Deployment's service does.
// The Starter is it, so Deployment's release execution can fill a
// deployment started from a copy; the Service shares the same instance.
type core struct {
	db                *database.DB
	authority         access.Authorizer
	still             operations.Authorities
	authorityProjects access.ProjectResources
	targets           *deployment.Targets
	needs             deployment.NeedStore
	hosts             *deployment.Hosts
	env               *deployment.Environment
	providers         *deployment.Providers
	catalog           *dependency.Catalog
	secrets           *secret.Service
	// owned reads a deployment's owned list: its object storage bucket is
	// recorded there, never as a managed need instance.
	owned *deployment.Deployments
	// sources reads a release's source and contract: what the release a
	// deployment runs declares, and a restored copy's release.
	sources deployment.ReleaseSources
	dataDir string
	// clock is AppTown's clock: every time a rule stores and later
	// judges (copy freshness, drills due, restored copies' expiry) is
	// written and read by it, so a test can set it. nil is the real clock.
	clock func() time.Time
	// hostScope reaches a rehearsal host's daemon; nil is the host's own
	// scope. A test points an attached host at a local daemon.
	hostScope func(context.Context, *deployment.Target) (docker.Scope, error)
}

// now reads AppTown's clock.
func (s *core) now() time.Time { return clock.Or(s.clock)() }

// manifest is a set's manifest: its record, naming AppTown that took it.
// BackupManifestFormat and BackupManifestVersion name the manifest a set
// carries beside its copies. AppTown reads its records, not the manifest;
// the manifest is for a person or a tool holding the set without AppTown,
// so it says what it is and where the set lives relative to its root.
const (
	BackupManifestFormat  = "flokkit.backup-set"
	BackupManifestVersion = 1
)

// backupManifest is a set's record as written beside its copies: the record's
// fields, its format and version, and its path relative to the set's root
// (the data directory's backups/ for a set on AppTown's disk, the bucket for
// a set in the backup store), never a path on this machine.
type backupManifest struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Backup
	Path string `json:"path"`
}

func (s *core) manifest(ctx context.Context, b Backup) ([]byte, error) {
	instance, err := identity.AppTownIDIn(ctx, s.db, s.now())
	if err != nil {
		return nil, err
	}
	b.AppTown = instance
	path := b.Path
	if b.Store != StoreBackupStore {
		rel, err := filepath.Rel(DiskDirectory(s.dataDir), b.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("set %s is not under AppTown's backups: %s", b.ID, b.Path)
		}
		path = filepath.ToSlash(rel)
	}
	return json.MarshalIndent(backupManifest{Format: BackupManifestFormat, Version: BackupManifestVersion, Backup: b, Path: path}, "", "  ")
}

// StarterDependencies are what a fill from a copy runs on.
type StarterDependencies struct {
	Authority   access.Authorizer
	Still       operations.Authorities
	Projects    access.ProjectResources
	Targets     *deployment.Targets
	Hosts       *deployment.Hosts
	Environment *deployment.Environment
	Providers   *deployment.Providers
	// Catalog is the dependency catalog: each kind's copy, import and verify.
	Catalog *dependency.Catalog
	// Deployments is Deployment's records (the instance Deployment uses,
	// with AppTown's clock): a deployment's bucket is on its owned list.
	Deployments *deployment.Deployments
	// Sources reads a release's source and contract (what a deployment's
	// running release declares; a restored copy's release).
	Sources deployment.ReleaseSources
	Secrets *secret.Service
	// DataDir is AppTown's data directory: sets on its disk and copies
	// staged on their way to and from the store are under it.
	DataDir string
	Now     func() time.Time
}

// Starter is data protection's side of starting a deployment from a copy
// (deployment.CopyStarter). It is built before Deployment's service, which
// is given it, and the data protection Service is built over the same one.
type Starter struct{ *core }

// NewStarter builds the fill a deployment started from a copy runs.
func NewStarter(db *database.DB, deps StarterDependencies) *Starter {
	owned := deps.Deployments
	if owned == nil {
		owned = deployment.NewDeployments(db) // a reader only: it stamps nothing
	}
	return &Starter{&core{db: db, authority: deps.Authority, still: deps.Still, authorityProjects: deps.Projects, targets: deps.Targets, needs: deployment.NewNeedStore(db, deps.Now), hosts: deps.Hosts, env: deps.Environment,
		providers: deps.Providers, catalog: deps.Catalog, secrets: deps.Secrets, owned: owned, sources: deps.Sources, dataDir: deps.DataDir, clock: deps.Now}}
}

// Service owns the answer to what data is held, whether it has been copied
// and witnessed coming back, and which data effects a restore or removal
// may perform: ordinary and final Docker copies, managed copies, the
// backup store with its pruning and purge, in-place restore, drills,
// sealed-host rehearsals, restored copies and their gate, and take-down
// orchestration. Deployment settles the placements and retained resources
// these touch, through its participants.
type Service struct {
	*core
	starter      *Starter
	deployments  *deployment.Deployments
	releases     *deployment.Releases
	bundles      *deployment.Bundles
	retained     *deployment.Retained
	outcomes     *deployment.Outcomes
	backups      *Backups
	projects     *project.Projects
	integrations *integration.Service
	ops          *operations.Service
	restored     RestoredCopyDependencies
	applied      func()
	// dataWork is the deployments a restore or a removal is changing now.
	dataWork    dataWork
	natives     sync.Map      // instance → nativeRead: the provider's answer about its backups
	nativeFresh time.Duration // how long that answer is shown (NativeReadFor)
}

// Dependencies are what data protection reads and runs on besides the
// Starter's: Deployment's records (the same instances Deployment uses),
// the release sources a restored copy reads its release from, the
// operations ledger, and restored copies' gate and secrets.
type Dependencies struct {
	Deployments  *deployment.Deployments
	Releases     *deployment.Releases
	Bundles      *deployment.Bundles
	Retained     *deployment.Retained
	Outcomes     *deployment.Outcomes
	Projects     *project.Projects
	Integrations *integration.Service
	// Operations is the ledger copies, drills, take-downs, rehearsals and
	// restored copies run through; nil: none can start.
	Operations *operations.Service
	// RestoredCopies are restored copies' gate and secrets; a zero value
	// makes none (restored_copy.go).
	RestoredCopies RestoredCopyDependencies
	// Applied is told an effect may have changed what a target holds.
	Applied func()
}

// New builds data protection over the Starter's records and collaborators
// and registers its operation kinds with the ledger.
func New(starter *Starter, deps Dependencies) *Service {
	s := &Service{core: starter.core, starter: starter, deployments: deps.Deployments, releases: deps.Releases, bundles: deps.Bundles,
		retained: deps.Retained, outcomes: deps.Outcomes, backups: NewBackups(starter.db), projects: deps.Projects, integrations: deps.Integrations,
		ops: deps.Operations, restored: deps.RestoredCopies, applied: deps.Applied, nativeFresh: NativeReadFor}
	s.backups.clock = starter.clock
	s.registerKinds()
	return s
}

// Manifests retain the portable backup format, without UI display labels.
func (m backupManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		backupFields
		StatusLabel *BackupLabel `json:"statusLabel,omitempty"`
		Format      string       `json:"format"`
		Version     int          `json:"version"`
		Path        string       `json:"path"`
	}{backupFields: backupFields(m.Backup), Format: m.Format, Version: m.Version, Path: m.Path})
}
