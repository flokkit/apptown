package fly

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/trust"
)

// Driver is the Fly Machines target for one deployment, with every
// collaborator injected.
type Driver struct {
	Dependencies
	options   Options
	frontDoor *http.Client
}

// Dependencies select one deployment's account and records.
type Dependencies struct {
	// Needs resolves catalogue containers and consumer variables at the supplied
	// per-need hosts, using this deployment's generated credentials.
	Needs func(context.Context, *appcontract.Contract, map[string]string) ([]deploy.ServiceSpec, map[string]string, error)
	// Client resolves the target's token and configuration at use time.
	Client func(context.Context) (*Client, Config, error)
	// Installation is this installation's id: every Machine carries it,
	// and it names the deployment's app.
	Installation string
	// Owner records the deployment's Fly app on its owned list as soon as
	// Fly answers its creation, so a take-down finds it even when no
	// release recorded it on the deployment. Without it no app is made.
	Owner deploy.Owner
	// Owned is the deployment's owned list as recorded.
	Owned func(context.Context) ([]deploy.Owned, error)
	// ReleaseLogin is the credential a release's images are read with from
	// their registry; nil or empty reads anonymously.
	ReleaseLogin func(ref string) (oci.Credential, error)
}

// Options configures one provider instance; zero values are production's.
type Options struct {
	APIBase      string
	registryHost string // empty is registry.fly.io; a test can supply its registry
	// pollInterval is how often a Machine's state is read while waiting.
	pollInterval time.Duration
	// machineWait bounds waiting for one Machine to start, pass its checks
	// or exit; zero is ten minutes.
	machineWait time.Duration
	// CopyImage copies an image by digest into Fly's registry (to is the
	// repository, with Fly's credential) and answers the digest it holds.
	CopyImage func(ctx context.Context, from oci.Source, to oci.Repository, tag string) (string, error)
	// FrontDoor answers the app's public address; nil is the deployments'
	// trust transport.
	FrontDoor http.RoundTripper
	// Now is the driver's clock for its deadlines; nil is the real one.
	Now func() time.Time
}

// NewDriver snapshots settings and collaborators.
func NewDriver(deps Dependencies, options Options) *Driver {
	if options.registryHost == "" {
		options.registryHost = registryHost
	}
	if options.pollInterval <= 0 {
		options.pollInterval = 5 * time.Second
	}
	if options.machineWait <= 0 {
		options.machineWait = 10 * time.Minute
	}
	if options.CopyImage == nil {
		options.CopyImage = func(ctx context.Context, from oci.Source, to oci.Repository, tag string) (string, error) {
			got, err := oci.Copy(ctx, from, to, tag, Platform)
			return got.Digest, err
		}
	}
	options.Now = clock.Or(options.Now)
	transport := options.FrontDoor
	if transport == nil {
		transport = trust.Transport(trust.Deployments)
	}
	return &Driver{Dependencies: deps, options: options, frontDoor: &http.Client{Timeout: 15 * time.Second, Transport: transport}}
}

// The driver implements what it is registered under.
var (
	_ deploy.Driver            = (*Driver)(nil)
	_ deploy.Renderer          = (*Driver)(nil)
	_ deploy.Preflighter       = (*Driver)(nil)
	_ deploy.ResourceFitter    = (*Driver)(nil)
	_ deploy.RuntimeController = (*Driver)(nil)
	_ deploy.RunningRecord     = (*Driver)(nil)
)

// Controls: Fly restarts a part's Machine whenever its process exits
// (restart policy always, without limit, as every other target restarts a
// part's container) and AppTown waits on each new Machine itself. Fly's checks do
// not restart a Machine, so liveness is not the target's: a liveness probe
// is refused (RefusesHealth).
func (d *Driver) Controls() deploy.Controls { return deploy.Controls{Liveness: false, Rollout: true} }

func (d *Driver) client(ctx context.Context) (*Client, Config, error) {
	if d.Client == nil {
		return nil, Config{}, deploy.Terminal(errors.New("no Fly account for this target"))
	}
	return d.Client(ctx)
}

func terminal(err error) error { return deploy.Terminal(err) }

// RecordedRunning is the generation the placement recorded running.
func (d *Driver) RecordedRunning(p deploy.Placement, _ string) string {
	g, _ := handleGeneration(p.Handle)
	if g == 0 {
		return ""
	}
	return generationWord(g)
}

// AdoptRunning makes the generation Fly runs the recorded one.
func (d *Driver) AdoptRunning(running string) map[string]any {
	g, ok := parseGeneration(running)
	if !ok {
		return nil
	}
	return map[string]any{"generation": g}
}
