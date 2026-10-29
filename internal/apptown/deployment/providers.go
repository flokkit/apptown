package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/oci"
)

// Providers builds the driver that runs a deployment on its target, and
// what can say what a target holds, for each kind of target: a Docker
// host (this AppTown's own or one attached over ssh), Portainer, App
// Platform and an S3-compatible bucket. Provider credentials are resolved
// from the secrets store and the connectors at use time, for one
// operation. It also answers whether a target or a cloud account answers
// (CheckTarget, CheckCloudAccount).
//
// One instance serves every caller; its factories are fixed at
// construction and it keeps no state of its own.
type Providers struct {
	db           *database.DB
	clock        func() time.Time // AppTown's clock; nil is the real one
	targets      *Targets
	deployments  *Deployments
	needs        NeedStore
	hosts        *Hosts
	env          *Environment
	projects     *project.Projects
	secrets      *secret.Service
	integrations *integration.Service
	// applied is told an effect may have changed what a target holds.
	applied      func()
	drivers      map[string]DriverFactory
	inventories  map[string]InventoryFactory
	digitalOcean digitalocean.Options
	s3Bucket     s3bucket.Options
	// afterRelease is told a Docker host or Portainer released a deployment;
	// databaseContainer is AppTown's own database, which the built-in
	// host's inventory knows is there.
	afterRelease      func(projectID ids.ID, deployment string)
	databaseContainer string
	installation      string
	// markers mark in place what records name, by kind (MarkRecorded).
	markers map[string]markerFactory
	// portainerProxies are the Docker proxies data work in Portainer
	// environments runs through, by target and environment.
	portainerMu      sync.Mutex
	portainerProxies map[string]*portainer.DockerProxy
}

// ProviderDependencies are the providers' owners and settings.
type ProviderDependencies struct {
	Projects     *project.Projects
	Secrets      *secret.Service
	Integrations *integration.Service
	DigitalOcean digitalocean.Options
	S3Bucket     s3bucket.Options
	// Drivers and Inventories replace the built-in factories for a kind
	// (a test's fake provider); the rest are built here.
	Drivers           map[string]DriverFactory
	Inventories       map[string]InventoryFactory
	AfterRelease      func(projectID ids.ID, deployment string)
	DatabaseContainer string
	// Applied is told an effect may have changed what a target holds (the
	// inventory cache); nil is fine.
	Applied func()
	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
	// Installation is this installation's id: what it makes at a target
	// carries it, and only what carries it can be not accounted for.
	Installation string
}

// DriverFactory builds the driver that runs one deployment on its target.
type DriverFactory func(ctx context.Context, t *Target, dep *Deployment) (deploy.Driver, error)

// InventoryFactory builds what can say what a target holds; it needs no
// deployment and cannot deploy.
type InventoryFactory func(t *Target) (deploy.Inventorier, error)

// NewProviders builds the providers over the deployment records they
// write (what a deployment owns, its buckets and need instances), the
// hosts they reach and the environment their boots are given.
func NewProviders(ctx context.Context, db *database.DB, records ProviderRecords, hosts *Hosts, env *Environment, deps ProviderDependencies) *Providers {
	p := &Providers{db: db, clock: deps.Now, targets: records.Targets, deployments: records.Deployments, needs: NeedStore{db: db, clock: deps.Now}, applied: deps.Applied,
		hosts: hosts, env: env, projects: deps.Projects, secrets: deps.Secrets, integrations: deps.Integrations,
		drivers: maps.Clone(deps.Drivers), inventories: maps.Clone(deps.Inventories),
		digitalOcean: deps.DigitalOcean, s3Bucket: deps.S3Bucket,
		afterRelease: deps.AfterRelease, databaseContainer: deps.DatabaseContainer, installation: deps.Installation}
	// The DigitalOcean driver judges its deadlines and grace on AppTown's
	// clock too, unless its options name a clock of their own.
	if p.digitalOcean.Now == nil {
		p.digitalOcean.Now = deps.Now
	}
	if p.drivers == nil {
		p.drivers = map[string]DriverFactory{}
	}
	if p.inventories == nil {
		p.inventories = map[string]InventoryFactory{}
	}
	if p.drivers[KindDockerHost] == nil {
		p.drivers[KindDockerHost] = p.dockerDriver
	}
	if p.inventories[KindDockerHost] == nil {
		p.inventories[KindDockerHost] = p.dockerInventory
	}
	p.configureProviders(ctx)
	return p
}

// ProviderRecords are the deployment records the providers' drivers write.
type ProviderRecords struct {
	Targets     *Targets
	Deployments *Deployments
}

// Driver is the registered driver for a deployment on its target's kind.
func (p *Providers) Driver(ctx context.Context, t *Target, dep *Deployment) (deploy.Driver, error) {
	if dep == nil {
		return nil, errors.New("a driver is built for a deployment; ask TargetInventory what a target holds")
	}
	f, ok := p.drivers[t.Kind]
	if !ok {
		return nil, fmt.Errorf("no driver for target kind %q", t.Kind)
	}
	return f(ctx, t, dep)
}

// TargetInventory is what can say what a target holds, for its kind. A
// kind with no reader is deploy.ErrNoInventory: unknown, not a failure.
func (p *Providers) TargetInventory(t *Target) (deploy.Inventorier, error) {
	f, ok := p.inventories[t.Kind]
	if !ok {
		return nil, deploy.ErrNoInventory
	}
	return f(t)
}

func (p *Providers) dockerDriver(ctx context.Context, t *Target, dep *Deployment) (deploy.Driver, error) {
	owner, err := project.ByIDIn(ctx, p.db, dep.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("reading the deployment's serving identity: %w", err)
	}
	return &docker.Driver{
		ServingLabel: owner.ServingLabel,
		Scope:        func(ctx context.Context) (docker.Scope, error) { return p.hosts.DockerScope(ctx, t) },
		Boot:         p.env.Host(dep.ProjectID, p.env.NeedCredentials(NeedSecretsScope(dep.ID)), func(slug string) KeyOwner { return deploymentKeys(p.db, p.clock, dep, slug) }),
		URL: func(servingLabel, name string, port int) (string, error) {
			reach, err := dockerReach(p.env.RoutesFor(ctx, dep.ProjectID), t, servingLabel, name, port)
			return reach.URL, err
		},
		AfterRelease: func(pl deploy.Placement) {
			if p.afterRelease != nil {
				p.afterRelease(dep.ProjectID, pl.Name)
			}
		},
		Registry: func() (*docker.RegistryCredential, error) { return p.env.ReleaseLogin(ctx, dep.ProjectID) },
		Scratch:  p.hosts.BootScratch(),
		Owner:    ownedRecorder{db: p.db, deployment: dep.ID, now: p.clock},
		Storage:  storageRecorder{db: p.db, deployment: dep.ID, now: p.clock},
		Owned: func(ctx context.Context) ([]deploy.Owned, error) {
			return ownedIn(ctx, deploymentsql.New(p.db.SQL()), dep.ID)
		},
		DataClass: dep.DataClass,
	}, nil
}

func (p *Providers) dockerInventory(t *Target) (deploy.Inventorier, error) {
	var also []string
	if t.Origin == OriginBuiltIn && p.databaseContainer != "" {
		also = []string{p.databaseContainer}
	}
	return docker.Inventory{Scope: func(ctx context.Context) (docker.Scope, error) { return p.hosts.DockerScope(ctx, t) }, Also: also,
		Images: func(ctx context.Context) ([]string, error) { return p.recordedImages(ctx, t) }}, nil
}

// recordedImages is the images the deployments on t recorded creating or
// selecting: what the inventory asks the host for (Inventory.Recorded lays
// the same records under its answer).
func (p *Providers) recordedImages(ctx context.Context, t *Target) ([]string, error) {
	deps, err := p.deployments.OnTarget(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("reading the deployments on %s: %w", t.Name, err)
	}
	var refs []string
	for _, d := range deps {
		owned, err := ownedIn(ctx, deploymentsql.New(p.db.SQL()), d.ID)
		if err != nil {
			return nil, fmt.Errorf("reading what %s/%s owns: %w", d.Project, d.Name, err)
		}
		for _, o := range owned {
			if o.Kind == "image" {
				refs = append(refs, o.ID)
			}
		}
	}
	return refs, nil
}

// doDocker adapts the built-in host's docker scope to what the driver asks
// of a container runtime.
type doDocker struct{ dk docker.Scope }

func (d doDocker) Command(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	return d.dk.Command(ctx, docker.Command{Args: args, Stdin: stdin})
}
func (d doDocker) ImageDigest(ctx context.Context, ref string) string {
	return d.dk.ImageDigest(ctx, ref)
}

// DigitalOceanConfig reads the current connector settings, without resolving secrets.
func (p *Providers) DigitalOceanConfig(ctx context.Context, provider string) (digitalocean.Config, error) {
	cfg, err := p.integrations.ConnectorConfig(ctx, provider, digitalocean.CapCloudAccount)
	if err != nil {
		return digitalocean.Config{}, err
	}
	if cfg == nil {
		return digitalocean.Config{}, fmt.Errorf("no cloud-account connector named %q", provider)
	}
	return digitalOceanConfiguration(cfg), nil
}

func digitalOceanConfiguration(cfg map[string]any) digitalocean.Config {
	str := func(k, def string) string {
		if v, _ := cfg[k].(string); v != "" {
			return v
		}
		return def
	}
	return digitalocean.Config{
		TokenFrom:    str("tokenFrom", "DIGITALOCEAN_ACCESS_TOKEN"),
		Region:       str("region", "syd"),
		Registry:     str("registry", ""),
		InstanceSize: str("instanceSize", "basic-xxs"),
		DBSize:       str("dbSize", "db-s-1vcpu-1gb"),
	}
}

// DigitalOceanClient resolves the named credential for this operation and context.
func (p *Providers) DigitalOceanClient(ctx context.Context, cfg digitalocean.Config) (*digitalocean.Client, error) {
	values, err := p.secrets.WithContext(ctx)
	if err != nil {
		return nil, err
	}
	tok := values[cfg.TokenFrom]
	if tok == "" {
		return nil, fmt.Errorf("the DigitalOcean token %s does not resolve — add it to the secrets store", cfg.TokenFrom)
	}
	return digitalocean.NewClient(tok, p.digitalOcean.APIBase), nil
}

// PortainerConfig resolves one connection snapshot and verifies the pinned
// destination before returning credentials to any provider operation.
func (p *Providers) PortainerConfig(ctx context.Context, target *Target) (*portainer.Target, error) {
	if target == nil || target.ID.IsZero() {
		return nil, errors.New("Portainer target has no recorded association")
	}
	t, err := p.targets.ByID(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if t.Kind != KindPortainer || t.ConnectionID.IsZero() || len(t.ProviderScope) == 0 {
		return nil, errors.New("Portainer target needs an explicit provider association before use")
	}
	var scope portainer.Scope
	if err := json.Unmarshal(t.ProviderScope, &scope); err != nil {
		return nil, errors.New("Portainer target scope is malformed")
	}
	cfg, err := p.integrations.PortainerConnection(ctx, t.ConnectionID)
	if err != nil {
		return nil, err
	}
	// VerifyScope rejects changed URL locally before the first authenticated GET.
	if err := portainer.VerifyScope(ctx, *cfg, scope); err != nil {
		return nil, err
	}
	cfg.PortainerURL = scope.APIBase
	cfg.Scope = &scope
	cfg.AppsDomain, _ = t.Ingress["domain"].(string)
	cfg.EnvName, _ = t.Address[AddressEnvironment].(string)
	cfg.Installation = p.installation
	return cfg, nil
}

// PortainerDataScope is a Docker scope for data work in a Portainer
// target's environment: the Docker CLI talks to a private socket this
// AppTown serves, which forwards each request to the environment's Docker
// through Portainer with the target's key (portainer.StartDockerProxy).
// The scope carries no attached streams, so a copy's streamed steps run as
// jobs (docker.Scope.NoStreams). An environment AppTown cannot reach for
// data is refused with portainer.ErrCannotBackUp and why. The proxy lives
// as long as AppTown does, one per target and environment.
func (p *Providers) PortainerDataScope(ctx context.Context, t *Target) (docker.Scope, error) {
	if t.DataAccess() != DataThroughPortainer {
		return docker.Scope{}, fmt.Errorf("%s is not a Portainer target", t.Name)
	}
	cfg, err := p.PortainerConfig(ctx, t)
	if err != nil {
		return docker.Scope{}, err
	}
	env, err := portainer.DataReach(ctx, cfg)
	if err != nil {
		return docker.Scope{}, err
	}
	key := fmt.Sprintf("%s/%d", t.ID, env.ID)
	p.portainerMu.Lock()
	defer p.portainerMu.Unlock()
	proxy := p.portainerProxies[key]
	if proxy == nil {
		// This socket is local IPC for the Docker CLI, never a container bind mount.
		// Keep it on the process filesystem and below Unix socket path limits,
		// independently of a host-shared or network-mounted TMPDIR.
		dir, err := os.MkdirTemp("/tmp", "forge-portainer-")
		if err != nil {
			return docker.Scope{}, err
		}
		targetID := t.ID
		proxy, err = portainer.StartDockerProxy(dir, env, func(ctx context.Context) (*portainer.Target, error) {
			current, err := p.targets.ByID(ctx, targetID)
			if err != nil {
				return nil, err
			}
			return p.PortainerConfig(ctx, current)
		})
		if err != nil {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("removing failed proxy directory: %w", cleanupErr))
			}
			return docker.Scope{}, err
		}
		if p.portainerProxies == nil {
			p.portainerProxies = map[string]*portainer.DockerProxy{}
		}
		p.portainerProxies[key] = proxy
	}
	dk := p.hosts.settings.Scope.WithHost(proxy.Host(), "", "")
	dk.NoStreams = true
	return dk, nil
}

// S3BucketConfig is an s3-bucket target's store, from its own record: the
// endpoint, region, website address template and the names of the secrets
// holding its key pair.
func S3BucketConfig(t *Target) s3bucket.Config {
	str := func(k string) string { v, _ := t.Address[k].(string); return v }
	return s3bucket.Config{Endpoint: str("endpoint"), Region: str("region"), WebsiteTemplate: str("websiteEndpoint"), AccessKeyFrom: str("accessKeyFrom"), SecretKeyFrom: str("secretKeyFrom")}
}

// S3BucketClient resolves the target's key pair for one operation. The
// keys come from the secrets store and go nowhere else.
func (p *Providers) S3BucketClient(ctx context.Context, cfg s3bucket.Config) (*s3bucket.Client, error) {
	values, err := p.secrets.WithContext(ctx)
	if err != nil {
		return nil, err
	}
	access, secret := values[cfg.AccessKeyFrom], values[cfg.SecretKeyFrom]
	var missing []string
	if access == "" {
		missing = append(missing, cfg.AccessKeyFrom)
	}
	if secret == "" {
		missing = append(missing, cfg.SecretKeyFrom)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("these secret names don't resolve: %s — add them to the secrets store", strings.Join(missing, ", "))
	}
	return &s3bucket.Client{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKey: access, SecretKey: secret}, nil
}

func (p *Providers) newS3BucketDriver(ctx context.Context, t *Target, dep *Deployment) (*s3bucket.Driver, error) {
	proj, err := p.driverProject(ctx, dep)
	if err != nil {
		return nil, err
	}
	targetID := t.ID
	return s3bucket.NewDriver(s3bucket.Dependencies{
		Client:       p.s3BucketClientFor(targetID),
		Installation: p.installation,
		Slug:         proj.Slug,
		Own:          func(bucket string) error { return p.deployments.RecordOwned(ctx, dep.ID, "bucket", bucket) },
		ReleaseLogin: func(ref string) (oci.Credential, error) { return p.releaseCredential(ctx, dep.ProjectID, ref) },
		BrowserValues: func(ctx context.Context) (map[string]string, error) {
			if p.integrations == nil {
				return nil, nil
			}
			return p.integrations.BrowserEnvironment(ctx, dep.ProjectID)
		},
	}, p.s3Bucket), nil
}

// driverProject is the project a provider driver tags its resources
// with. A driver is never built without it: with an empty slug a
// DigitalOcean driver would tag clusters as no project's.
func (p *Providers) driverProject(ctx context.Context, dep *Deployment) (project.Project, error) {
	proj, err := p.projects.ByID(ctx, dep.ProjectID)
	if err != nil {
		return project.Project{}, fmt.Errorf("reading project %d for its deployment %s: %w", dep.ProjectID, dep.Name, database.Missing(err))
	}
	return proj, nil
}

func (p *Providers) newDOAppDriver(ctx context.Context, t *Target, dep *Deployment) (*digitalocean.Driver, error) {
	proj, err := p.driverProject(ctx, dep)
	if err != nil {
		return nil, err
	}
	connection, err := p.integrations.Bindings.ByName(ctx, t.Provider)
	if err != nil {
		return nil, fmt.Errorf("reading deployment connection: %w", err)
	}
	if !t.ConnectionID.IsZero() && t.ConnectionID != connection.ID {
		return nil, errors.New("deployment connection identity changed")
	}
	return digitalocean.NewDriver(digitalocean.Dependencies{
		ConnectionID: connection.ID,
		Docker:       doDocker{p.hosts.settings.Scope},
		Client:       p.cloudClientForID(connection.ID),
		Slug:         proj.Slug,
		Needs:        p.needs,
		// The deployment's bucket and Spaces key are on its owned list; the
		// key's secret is in the built-in store under its scope.
		Holdings: holdings{deployments: p.deployments, dep: dep},
		Secrets:  p.needSecrets(dep),
		// A project with no registry attached now reads anonymously, and
		// its registry's refusal is the release's error.
		ReleaseLogin: func(ref string) (oci.Credential, error) { return p.releaseCredential(ctx, dep.ProjectID, ref) },
		// The gate image is read with the project's registry credential
		// only when it lives on that registry's host; a credential is never
		// sent to another host.
		GateLogin:    func(ref string) (oci.Credential, error) { return p.releaseCredential(ctx, dep.ProjectID, ref) },
		Repository:   repositoryRecorder{db: p.db, project: dep.ProjectID, connector: t.Provider, connection: connection.ID, now: p.clock},
		Installation: p.installation,
	}, p.digitalOcean), nil
}

// cloudClientFor resolves the platform's credentials at use time, restricted
// to one connector.
func (p *Providers) cloudClientFor(provider string) func(context.Context) (*digitalocean.Client, digitalocean.Config, error) {
	return func(ctx context.Context) (*digitalocean.Client, digitalocean.Config, error) {
		if err := ctx.Err(); err != nil {
			return nil, digitalocean.Config{}, err
		}
		cfg, err := p.DigitalOceanConfig(ctx, provider)
		if err != nil {
			return nil, cfg, deploy.Terminal(err)
		}
		client, err := p.DigitalOceanClient(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cfg, ctx.Err()
			}
			return nil, cfg, deploy.Terminal(err)
		}
		return client, cfg, nil
	}
}

// cloudClientForID keeps a deployment's credential reads on its selected
// connection even if its former machine name is later reused.
func (p *Providers) cloudClientForID(connection ids.ID) func(context.Context) (*digitalocean.Client, digitalocean.Config, error) {
	return func(ctx context.Context) (*digitalocean.Client, digitalocean.Config, error) {
		config, err := p.integrations.ConnectorConfigByID(ctx, connection, digitalocean.CapCloudAccount)
		if err != nil {
			return nil, digitalocean.Config{}, deploy.Terminal(err)
		}
		cfg := digitalOceanConfiguration(config)
		client, err := p.DigitalOceanClient(ctx, cfg)
		return client, cfg, err
	}
}

// configureProviders binds provider drivers to Deployment's own stores. Credentials
// are resolved on use, and explicit factory overrides are preserved.
func (p *Providers) configureProviders(ctx context.Context) {
	if p.drivers[KindPortainer] == nil {
		p.drivers[KindPortainer] = func(ctx context.Context, t *Target, dep *Deployment) (deploy.Driver, error) {
			return &portainerRelease{providers: p, target: t, dep: dep, Driver: &portainer.Driver{Diagnostics: p.portainerDiagnostic, Now: p.clock, TargetID: t.ID, Owner: ownedRecorder{db: p.db, deployment: dep.ID, now: p.clock}, Config: func(ctx context.Context, id ids.ID) (*portainer.Target, error) {
				target, err := p.targets.ByID(ctx, id)
				if err != nil {
					return nil, fmt.Errorf("resolving Portainer target %d: %w", id, err)
				}
				return p.PortainerConfig(ctx, target)
			}}}, nil
		}
	}
	if p.drivers[KindDOApp] == nil {
		p.drivers[KindDOApp] = func(ctx context.Context, t *Target, d *Deployment) (deploy.Driver, error) {
			driver, err := p.newDOAppDriver(ctx, t, d)
			if err != nil {
				return nil, err
			}
			return &digitalOceanRelease{Driver: driver}, nil
		}
	}
	if p.drivers[KindS3Bucket] == nil {
		p.drivers[KindS3Bucket] = func(ctx context.Context, t *Target, d *Deployment) (deploy.Driver, error) {
			driver, err := p.newS3BucketDriver(ctx, t, d)
			if err != nil {
				return nil, err
			}
			return driver, nil
		}
	}
	if p.inventories[KindPortainer] == nil {
		p.inventories[KindPortainer] = func(t *Target) (deploy.Inventorier, error) {
			return portainer.Inventory{Config: func(ctx context.Context) (*portainer.Target, error) { return p.PortainerConfig(ctx, t) }}, nil
		}
	}
	p.markers = map[string]markerFactory{KindDOApp: p.markClusters, KindS3Bucket: p.markBuckets}
	if p.inventories[KindS3Bucket] == nil {
		p.inventories[KindS3Bucket] = func(t *Target) (deploy.Inventorier, error) {
			return s3bucket.Inventory{Client: p.s3BucketClientFor(t.ID), Installation: p.installation}, nil
		}
	}
	if p.inventories[KindDOApp] == nil {
		p.inventories[KindDOApp] = func(t *Target) (deploy.Inventorier, error) {
			return digitalocean.Inventory{Client: p.cloudClientFor(t.Provider), Installation: p.installation}, nil
		}
	}
}

// s3BucketClientFor resolves a bucket-website target's store and key pair
// at use time, from its current record.
func (p *Providers) s3BucketClientFor(targetID ids.ID) func(context.Context) (*s3bucket.Client, s3bucket.Config, error) {
	return func(ctx context.Context) (*s3bucket.Client, s3bucket.Config, error) {
		current, err := p.targets.ByID(ctx, targetID)
		if err != nil {
			return nil, s3bucket.Config{}, err
		}
		cfg := S3BucketConfig(current)
		c, err := p.S3BucketClient(ctx, cfg)
		if err != nil {
			return nil, cfg, deploy.Terminal(err)
		}
		return c, cfg, nil
	}
}

// ManagedData is the provider module that answers for a target's managed
// instances; nil when the target has none.
func (p *Providers) ManagedData(t *Target) deploy.ManagedData {
	if t.Kind != KindDOApp {
		return nil
	}
	return digitalocean.ManagedData{Client: p.cloudClientFor(t.Provider), OwnDatabase: p.digitalOcean.OwnDatabase}
}

// PrepareNeeds fulfils the contract's needs on the target and records
// the need instances (§4.1). Emulator targets fulfil at boot, in the
// driver; managed targets go through the driver's need module.
func (p *Providers) PrepareNeeds(ctx context.Context, d deploy.Driver, t *Target, dep *Deployment, c *appcontract.Contract, logf deploy.Logf) (deploy.NeedBindings, error) {
	out := deploy.NeedBindings{}
	if t.NeedsMode != "managed" || len(c.Needs) == 0 {
		return out, nil
	}
	f, ok := d.(deploy.NeedFulfiller)
	if !ok {
		return nil, fmt.Errorf("%s has no managed need module", t.Kind)
	}
	for _, need := range c.Needs {
		if k, err := p.env.registerKind(ctx, need); err != nil {
			return nil, err
		} else if k != nil {
			continue // fulfilled by registering, not by the target
		}
		b, handle, err := f.FulfilNeed(ctx, dep.Placement(), need, c.NeedParams[need], logf)
		if err != nil {
			return nil, fmt.Errorf("fulfilling %s on %s: %w", need, t.Name, err)
		}
		out[need] = b
		if handle == nil {
			// A need the driver keeps on the deployment's owned list (an
			// object store's bucket and key), not a managed instance.
			continue
		}
		// The need is fulfilled on the target: its record is written though
		// the caller has gone.
		if err := p.needs.RecordFulfilled(context.WithoutCancel(ctx), dep.ID, need, b.Native, handle); err != nil {
			return nil, err
		}
	}
	if p.applied != nil {
		p.applied()
	}
	return out, nil
}

// RefusesBundle says why b cannot be released to t, before anything is
// prepared. A digest bundle's images are in its registry: it goes to any
// target that runs its platform (App Platform runs linux/amd64; a Docker
// host's and Portainer's platform are asked where they are released), but
// never, in an AppTown with no local Docker, to the built-in host. In that
// mode a bundle of image IDs goes only to the attached Docker host it was
// recorded on: Portainer and App Platform need its images in a registry. A
// bucket needs them there in every mode, and Portainer's driver refuses
// them unpublished in every mode too.
func (p *Providers) RefusesBundle(ctx context.Context, b *ReleaseBundle, t *Target) error {
	if t.Kind == KindS3Bucket && (b == nil || !b.ByDigest()) {
		return refused("%s publishes a site's files from its release image in the project's container registry, and this release was not published there. Attach a container registry to the project, then land a change", t.Name)
	}
	appPlatform := t.Kind == KindDOApp
	if b != nil && appPlatform {
		if why := digitalocean.MultiImageRefusal(b.Parts, b.Init); why != "" {
			return refused("%s", why)
		}
	}
	if b != nil && b.ByDigest() {
		if appPlatform {
			if err := refusesPlatform(b, t.Name, digitalocean.Platform, nil); err != nil {
				return err
			}
		}
		if p.hosts.settings.NoLocalDocker && t.Kind == KindDockerHost && t.Origin != OriginAttached {
			return errBuiltInHostRefused
		}
		return nil
	}
	switch {
	case !p.hosts.settings.NoLocalDocker:
		return nil
	case t.Kind != KindDockerHost:
		return refused("releasing to %s (%s) needs this release's images in the project's container registry, and this release was not published there. Attach a container registry to the project, then land a change", t.Name, t.Kind)
	case t.Origin != OriginAttached:
		return errBuiltInHostRefused
	case b == nil || b.BuiltFrom.TargetID.IsZero():
		return refused("this release does not say which Docker host its images are on; land the change again to release it in this mode")
	case b.BuiltFrom.TargetID != t.ID:
		name := fmt.Sprintf("target %d", b.BuiltFrom.TargetID)
		if on, err := p.targets.ByID(ctx, b.BuiltFrom.TargetID); err == nil {
			name = on.Name
		}
		return refused("this release's images live only on %s", name)
	}
	return nil
}

// Running asks the driver as a fact when it can inspect, else observes.
func (p *Providers) Running(ctx context.Context, d deploy.Driver, dep *Deployment) (bool, error) {
	if i, ok := d.(deploy.Inspector); ok {
		return i.Inspect(ctx, dep.Placement())
	}
	o, err := d.Observe(ctx, dep.Placement())
	return o.Rollout == "active", err
}

// needSecrets is where dep's need credentials are kept; nil when AppTown
// has no secret store (a test's providers).
func (p *Providers) needSecrets(dep *Deployment) digitalocean.KeySecrets {
	if p.secrets == nil || p.secrets.Vault == nil {
		return nil
	}
	return needSecrets{vault: p.secrets.Vault, scope: NeedSecretsScope(dep.ID)}
}

// releaseCredential is the credential a project's release images are read
// with: its current repository's when the source is in scope, or none
// (anonymous). A registry that cannot be read is an error, never anonymous.
func (p *Providers) releaseCredential(ctx context.Context, projectID ids.ID, ref string) (oci.Credential, error) {
	repo, err := p.env.ReleaseRepository(ctx, projectID)
	if errors.Is(err, ErrNoReleaseRegistry) {
		return oci.Credential{}, nil
	}
	if err != nil {
		return oci.Credential{}, err
	}
	purpose, err := p.env.Purpose(ctx, projectID)
	if err != nil {
		return oci.Credential{}, err
	}
	if !releaseCredentialAllows(repo, ref, !purpose.Integrations()) {
		return oci.Credential{}, nil
	}
	return repo.Credential, nil
}
