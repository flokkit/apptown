package digitalocean

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/oci"
)

// Driver is the App Platform target: renderer, reconciler and the
// managed-database need module, with every collaborator injected.
type Driver struct {
	Dependencies
	options   Options
	frontDoor *http.Client // asks the app's front door, dialling with options.Dial
}

// Dependencies selects one deployment's account, runtime and durable need store.
type Dependencies struct {
	Docker Docker
	Client func(context.Context) (*Client, Config, error)
	Slug   string // the application, for naming on the platform
	Needs  deploy.NeedRecorder
	// ReleaseLogin is the credential a release's images by digest are read
	// from their registry with, when they are copied into DOCR; nil reads
	// anonymously.
	ReleaseLogin func() oci.Credential
	// GateLogin is the credential AppTown's gate image (by its
	// reference) is read with when a gated app is rendered; nil or an empty
	// credential reads it anonymously.
	GateLogin func(ref string) oci.Credential
	// Repository records the project's DOCR repository AppTown created on
	// this account; nil records nothing, and then a take-down never empties
	// it.
	Repository RepositoryOwner
	// Holdings is the deployment's owned list, where its bucket and Spaces
	// key are recorded; Secrets keeps the key's secret. Without them the s3
	// need is refused.
	Holdings Holdings
	Secrets  KeySecrets
}

// Options configures one provider instance. Zero settings use production defaults.
// Collaborators must be safe for concurrent calls if the instance is shared.
type Options struct {
	APIBase string
	// RegistryHost is DOCR's host; "" is registry.digitalocean.com.
	RegistryHost         string
	PollInterval, DBPoll time.Duration
	PushImage            func(*Driver, context.Context, *Client, string, string, string, string, func(string, ...any)) (string, error)
	// CopyImage copies an image by digest into DOCR (registry, repository,
	// source, tag) and answers the digest DOCR holds.
	CopyImage   func(*Driver, context.Context, *Client, string, string, oci.Source, string, func(string, ...any)) (string, error)
	OwnDatabase func(context.Context, string, string, string) error
	// ProbeFrontDoor asks a URL until it answers; until is the end of the
	// platform's new name's grace (zero: none).
	ProbeFrontDoor func(ctx context.Context, url string, until time.Time, logf func(string, ...any)) error
	// Resolve looks a host name up in DNS; nil is the system resolver.
	Resolve func(ctx context.Context, host string) error
	// Dial opens the front door checks' connections (host:port); nil is a
	// net.Dialer as the standard HTTP transport uses.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Now is the driver's clock, for its deadlines and the platform's new
	// name's grace; nil is the real clock.
	Now func() time.Time
	// SpacesEndpoint is Spaces' S3 endpoint in a region; nil is the real
	// one. BucketSuffix makes a bucket's name unique; nil is random.
	SpacesEndpoint func(region string) string
	BucketSuffix   func() string
	// GrantWait is how long a new bucket's key grant is asked again while
	// DigitalOcean refuses it; zero is a minute.
	GrantWait time.Duration
}

// NewDriver snapshots settings and collaborators; no process-wide hooks are read.
func NewDriver(deps Dependencies, options Options) *Driver {
	if options.PollInterval <= 0 {
		options.PollInterval = 10 * time.Second
	}
	if options.DBPoll <= 0 {
		options.DBPoll = 15 * time.Second
	}
	if options.RegistryHost == "" {
		options.RegistryHost = RegistryHost
	}
	if options.PushImage == nil {
		options.PushImage = (*Driver).pushImage
	}
	if options.CopyImage == nil {
		options.CopyImage = (*Driver).copyFrom
	}
	if options.OwnDatabase == nil {
		options.OwnDatabase = ownDatabase
	}
	if options.Dial == nil {
		options.Dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = options.Dial
	frontDoor := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	options.Now = clock.Or(options.Now)
	if options.SpacesEndpoint == nil {
		options.SpacesEndpoint = SpacesEndpoint
	}
	if options.BucketSuffix == nil {
		options.BucketSuffix = randomSuffix
	}
	if options.GrantWait <= 0 {
		options.GrantWait = time.Minute
	}
	if options.ProbeFrontDoor == nil {
		interval, now := options.PollInterval, options.Now
		options.ProbeFrontDoor = func(ctx context.Context, url string, until time.Time, logf func(string, ...any)) error {
			return probeFrontDoor(ctx, frontDoor, url, until, logf, interval, now)
		}
	}
	if options.Resolve == nil {
		options.Resolve = func(ctx context.Context, host string) error {
			_, err := net.DefaultResolver.LookupHost(ctx, host)
			return err
		}
	}
	return &Driver{Dependencies: deps, options: options, frontDoor: frontDoor}
}

func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	return d.reconcile(ctx, p, generation, art, key, logf, checkpoint)
}

func (d *Driver) Resume(ctx context.Context, p deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	return d.resume(ctx, p, generation, noted, logf)
}

func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	return d.observe(ctx, p)
}

func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, generation int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	var out deploy.TakenDown
	if err := d.teardown(ctx, p, generation); err != nil {
		return out, err
	}
	if id, _ := p.Handle["appId"].(string); id != "" {
		out.Deleted = append(out.Deleted, "app "+id)
	}
	deleted, left := d.emptyRepository(ctx, td)
	out.Deleted = append(out.Deleted, deleted...)
	out.Left = append(out.Left, left...)
	deleted, left = d.tearDownStorage(ctx, p, td)
	out.Deleted = append(out.Deleted, deleted...)
	out.Left = append(out.Left, left...)
	return out, nil
}

func (d *Driver) Controls() deploy.Controls {
	return deploy.Controls{Liveness: true, Rollout: true}
}

// The driver implements the contract it is registered under.
var (
	_ deploy.Driver        = (*Driver)(nil)
	_ deploy.Renderer      = (*Driver)(nil)
	_ deploy.NeedFulfiller = (*Driver)(nil)
)

// Inventory answers what the account holds at the platform: apps,
// databases and the registry. Built for a target, it can look, never deploy.
type Inventory struct {
	Client func(context.Context) (*Client, Config, error)
}

var _ deploy.Inventorier = Inventory{}

// RemoveApp deletes the app with this id, one AppTown recorded creating
// (an attempt's checkpoint names it). It is how a gated app whose first
// deployment never went active is taken down: the ordinary teardown
// refuses an app whose active deployment is not the recorded one, and a
// gated app holds production data that must not stay. An app already gone
// is removed.
func (d *Driver) RemoveApp(ctx context.Context, appID string) error {
	if appID == "" {
		return nil
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	if err := c.Call(ctx, "DELETE", "/v2/apps/"+appID, nil, nil); err != nil && !IsStatus(err, 404) {
		return err
	}
	return nil
}

// Made is what a gated deployment's start may have made on the account,
// found by the names and markers AppTown gives what it makes: its app
// (the deployment's app name) and its managed clusters (the need's cluster
// name, carrying this project's and deployment's AppTown tags). It is how a
// take-down finds what it must delete when an identity was never recorded:
// the platform answered a create, and AppTown failed or stopped before
// it wrote the answer down. A lookup that fails is an error, never "none".
type Made struct {
	AppID    string
	Clusters []MadeCluster
	// NotOurs names what was found by name but does not carry this
	// deployment's mark: it is somebody else's, and is never deleted.
	NotOurs []string
}

// carriesMarker says whether an app's gate carries the mark.
func carriesMarker(app *doApp, marker string) bool {
	for _, svc := range app.Spec.Services {
		if svc.Name != GateComponent {
			continue
		}
		for _, e := range svc.Envs {
			if e.Key == deploy.GateEnvMarker && e.Value == marker {
				return true
			}
		}
	}
	return false
}

// MadeCluster is one cluster found by name and markers.
type MadeCluster struct{ ID, Name string }

// FindMade looks up what p's start may have made, by name.
func (d *Driver) FindMade(ctx context.Context, p deploy.Placement) (Made, error) {
	var out Made
	c, _, err := d.Client(ctx)
	if err != nil {
		return out, err
	}
	appName := doAppName(deploy.HostKey(p.ID))
	app, err := d.findApp(ctx, c, appName)
	if err != nil {
		return out, fmt.Errorf("looking for the app by name: %w", err)
	}
	// A name is not ownership: two projects' names can join to the same app
	// name. Only an app carrying this deployment's own mark is its.
	switch {
	case app == nil:
	case p.Gate != nil && p.Gate.Marker != "" && carriesMarker(app, p.Gate.Marker):
		out.AppID = app.ID
	default:
		out.NotOurs = append(out.NotOurs, fmt.Sprintf("an app named %s (%s) without this copy's mark", appName, app.ID))
	}
	name := clusterName(deploy.HostKey(p.ID))
	db, err := d.findDatabase(ctx, c, name)
	if err != nil {
		return out, fmt.Errorf("looking for the cluster by name: %w", err)
	}
	// A cluster of that name without this deployment's markers is not
	// AppTown's to delete.
	tags := clusterTags(d.Slug, p.Name)
	if db != nil && containsString(db.Tags, tags[1]) && containsString(db.Tags, tags[2]) {
		out.Clusters = append(out.Clusters, MadeCluster{ID: db.ID, Name: db.Name})
	}
	return out, nil
}
