package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Driver is a Docker host's driver: it builds the selected source on the
// host, or runs the images the source names (deploy.Source.Images: IDs on
// this daemon, or images by digest it pulls), and
// runs it as the placement's container, through the stack boot it is given.
type Driver struct {
	ServingLabel string                                                 // immutable owner-supplied routing label, independent of source.Slug
	Scope        func(context.Context) (Scope, error)                   // which daemon: AppTown's own, or an attached host's
	Boot         Booter                                                 // the stack boot
	URL          func(servingLabel, deployment string, port int) string // the address a deployment is reached at
	AfterRelease func(p deploy.Placement)                               // what follows a release (a backup)
	// Registry is the credential a release's images by digest are pulled
	// with, and Scratch the folder each pull's own Docker configuration is
	// written under (RecordedImages). A credential that cannot be read is
	// an error, never an anonymous pull.
	Registry func() (*RegistryCredential, error)
	Scratch  string
	// Owner records what the deployment's boot creates on the host; nil
	// records nothing, and then a take-down deletes only the container.
	Owner          deploy.Owner
	Storage        deploy.StorageBindings
	RestoreStorage deploy.StorageRestore
	// Owned reads the deployment's list, so a boot resets only what the
	// AppTown recorded making; nil reads nothing.
	Owned func(context.Context) ([]deploy.Owned, error)
	// DataClass is what the deployment declares it holds; only
	// "synthetic" lets a standing service be replaced when its declared
	// parameters move. "" is unknown, and protected.
	DataClass string
	// Fill, when set, puts the deployment's data back in this boot, after
	// its needs and before anything of the app starts (BootRequest.Fill):
	// a rebuild after AppTown was restored.
	Fill func(network string) error
}

// Booter runs the one boot sequence for a request and reports the port
// and the public image that came up.
type Booter interface {
	Boot(ctx context.Context, req BootRequest, c *appcontract.Contract) (BootResult, error)
}

var _ deploy.Driver = (*Driver)(nil)

func (d *Driver) Resume(ctx context.Context, p deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	dk, err := d.Scope(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	return Resume(ctx, dk, p, generation, noted)
}

func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	if art.Kind != "docker-source" || art.Source == nil || art.Source.Contract == nil || art.Hash != deploy.ConfigHash(string(art.Body)) {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("Docker deployment requires prepared selected source"))
	}
	if d.ServingLabel == "" {
		return deploy.Observation{}, deploy.Terminal(errors.New("Docker deployment has no serving label"))
	}
	source := art.Source
	slug, ws, c := source.Slug, source.Workspace, source.Contract
	names, err := NamesOf(p.ID, p.Name)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the deployment's host names: %w", err))
	}
	name := names.App()
	id := deploy.DockerIdentity{Deployment: p.Label, Generation: generation, Artifact: art.Hash}
	if err := checkpoint(id.Checkpoint(name)); err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	if dk.Remote() {
		logf("booting %s on %s", name, dk.Hostname)
	} else {
		logf("booting %s on the built-in host", name)
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	req := BootRequest{
		Log:  logf,
		Name: name, StackID: names.Stack(), Slug: slug, ServingLabel: d.ServingLabel, Workspace: ws,
		Deployment: p.Name, DataClass: d.DataClass, HostPort: p.HostPort, Persistent: true, Probe: 90 * time.Second, Docker: dk,
		Identity: &id, Services: SourceServices(art), Revision: source.Revision, Fill: d.Fill, Storage: d.Storage, RestoreStorage: d.RestoreStorage,
	}
	if d.Owner != nil {
		req.Record = func(o deploy.Owned) error { return d.Owner.Created(ctx, o) }
	}
	if d.Owned != nil {
		if req.Owned, err = d.Owned(ctx); err != nil {
			return deploy.Observation{}, deploy.Terminal(fmt.Errorf("reading what the deployment owns: %w", err))
		}
	}
	if source.Images != nil {
		// The release's images, on this daemon or pulled by digest: run
		// them, build nothing.
		req.Recorded = &RecordedImages{Parts: source.Images, Init: source.Init, Scratch: d.Scratch}
		if d.Registry != nil {
			if req.Recorded.Login, err = d.Registry(); err != nil {
				return deploy.Observation{}, fmt.Errorf("the credential the release's images are pulled with: %w", err)
			}
		}
	}
	res, err := d.Boot.Boot(ctx, req, c)
	if err != nil && AsBootError(err).KnownOutcome() {
		return deploy.Observation{}, deploy.Terminal(err) // the boot said the new version did not come up
	}
	if err != nil {
		return deploy.Observation{}, err
	}
	url := d.URL(d.ServingLabel, p.Name, res.HostPort)
	if dk.Remote() {
		url = dk.FrontDoor(d.ServingLabel, p.Name, res.HostPort)
	}
	o, err := RecordBoot(ctx, dk, id, name, res.Image(), url, res.HostPort, checkpoint)
	if err != nil {
		return deploy.Observation{}, err
	}
	d.AfterRelease(p)
	return o, nil
}

func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	o := deploy.Observation{Generation: p.Generation, URL: p.URL, Rollout: "absent", Health: "unknown"}
	if p.Container == "" {
		return o, nil
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return o, err
	}
	runtime, err := Inspect(ctx, dk, p.Container)
	if errors.Is(err, ErrContainerAbsent) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	if runtime.Running {
		o.Rollout, o.Health = "active", "healthy"
	} else {
		o.Health = "unhealthy"
	}
	return o, nil
}

// Teardown removes the recorded generation's container, verified by its
// labels, then everything else the deployment's list names
// (TearDownOwned). The container is the serving thing: failing to remove
// it fails the teardown, and nothing else is touched.
func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, generation int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	var out deploy.TakenDown
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if p.Generation != generation {
		return out, fmt.Errorf("deployment is at generation %d, not %d — refusing to tear down what was not recorded", p.Generation, generation)
	}
	if p.Container == "" && len(td.Owned) == 0 {
		return out, nil
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return out, err
	}
	serving, err := removeServing(ctx, dk, p)
	if err != nil {
		return out, err
	}
	rest := TearDownOwned(ctx, dk, td.Owned, serving, td.BackedUp || td.Synthetic, d.Storage)
	if serving != "" {
		out.Deleted = append(out.Deleted, "container "+p.Container)
	}
	out.Deleted = append(out.Deleted, rest.Deleted...)
	out.Left = rest.Left
	return out, nil
}

// removeServing removes the deployment's app container and answers its ID,
// "" when it was already gone.
func removeServing(ctx context.Context, dk Scope, p deploy.Placement) (string, error) {
	if p.Container == "" {
		return "", nil
	}
	runtime, err := Inspect(ctx, dk, p.Container)
	if errors.Is(err, ErrContainerAbsent) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// The name can hold a container of another generation of this
	// deployment: one a failed release started. It is the deployment's to
	// remove, and no newer attempt can be starting one, because a take-down
	// is refused while an attempt is open. A container of anything else is
	// refused. The immutable ID is deleted so a later rename cannot retarget rm.
	if runtime.ID == "" || runtime.Labels[dockerlabel.Deployment] != string(p.Label) {
		return "", errors.New("the container under the deployment's name belongs to something else; refusing removal")
	}
	// Without -v: a container's anonymous volumes are not on the list, and
	// only what the list names is ever deleted.
	_, err = dk.Command(ctx, Command{Args: []string{"rm", "-f", runtime.ID}, Timeout: 30 * time.Second})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if _, inspectErr := Inspect(ctx, dk, runtime.ID); errors.Is(inspectErr, ErrContainerAbsent) {
			return runtime.ID, nil
		}
		return "", fmt.Errorf("removing deployment container: %w", err)
	}
	return runtime.ID, nil
}

func (d *Driver) Controls() deploy.Controls {
	return deploy.Controls{Liveness: false, Rollout: false}
}

// Inspect asks the host whether the deployment's container is running;
// an absent container is not running, a host that cannot be asked is an
// error.
func (d *Driver) Inspect(ctx context.Context, p deploy.Placement) (bool, error) {
	if p.Container == "" {
		return false, nil
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return false, err
	}
	runtime, err := Inspect(ctx, dk, p.Container)
	if errors.Is(err, ErrContainerAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return runtime.Running, nil
}

var _ deploy.Inspector = (*Driver)(nil)

// FitResources admits a release's declared resources against the daemon
// it runs on, before any effect (deploy.ResourceFitter): a part or need
// larger than the host is refused (AdmitResources). A Docker host holds
// each declaration exactly, so nothing is rounded: no part runs on a
// size of its own.
func (d *Driver) FitResources(ctx context.Context, c *appcontract.Contract) (map[string]string, error) {
	if c == nil || !declaresResources(c) {
		return nil, nil
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return nil, err
	}
	return nil, AdmitResources(ctx, dk, c)
}
