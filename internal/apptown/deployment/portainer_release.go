package deployment

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

// portainerTier is the one deployment name a project has on a Portainer
// target: the target's tier, and lan for one attached with none (the
// integration's own default). Stacks, volumes and routes are named for the
// project, so a second deployment there would take the first one's.
func portainerTier(t *Target) string {
	if tier, _ := t.Address["tier"].(string); tier != "" {
		return tier
	}
	return "lan"
}

// portainerRelease is the Portainer driver a deployment released by name is
// given: it renders the stack the intranet release renders
// (Environment.portainerStack), runs it at the address its hosts route, and
// asks for a backup once it is live, as the Docker host and the intranet
// release do.
type portainerRelease struct {
	*portainer.Driver
	providers *Providers
	target    *Target
	dep       *Deployment
}

// releaseRefuser is a driver that refuses a release of bundle to its
// deployment, asked before a numbered release is made and again before
// a release is prepared.
type releaseRefuser interface {
	refusesRelease(bundle *ReleaseBundle) error
}

// integrationsGiver is a driver that gives the app its project's attached
// integrations' values itself, as a Docker host does.
type integrationsGiver interface {
	givesIntegrations()
}

// givesIntegrations: the stack carries them (Environment.portainerStack).
func (d *portainerRelease) givesIntegrations() {}

func (d *portainerRelease) refusesRelease(bundle *ReleaseBundle) error {
	if tier := portainerTier(d.target); d.dep.Name != tier {
		return refused("a project has one deployment on %s, %s, because its stack and volumes are named for the project; release that one", d.target.Name, tier)
	}
	if bundle == nil || !bundle.ByDigest() {
		return notPublished(d.target.Name)
	}
	return nil
}

func (d *portainerRelease) Render(ctx context.Context, pl deploy.Placement, src deploy.Source, _ deploy.NeedBindings, secrets deploy.ConsumerSecrets, logf deploy.Logf) (deploy.Artifact, error) {
	if tier := portainerTier(d.target); d.dep.Name != tier {
		return deploy.Artifact{}, refused("a project has one deployment on %s, %s, because its stack and volumes are named for the project; release that one", d.target.Name, tier)
	}
	if !src.Published {
		return deploy.Artifact{}, notPublished(d.target.Name)
	}
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return deploy.Artifact{}, err
	}
	if cfg.AppsDomain == "" {
		return deploy.Artifact{}, refused("the %s target names no domain to answer on: set one in its settings", d.target.Name)
	}
	endpointID, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		return deploy.Artifact{}, err
	}
	platform, asked := portainer.EndpointPlatform(ctx, cfg, endpointID)
	if err := refusesBuiltPlatform(src.Platform, d.target.Name, platform, asked); err != nil {
		return deploy.Artifact{}, err
	}
	logf("%s runs %s", d.target.Name, platform)
	vanity := releaseProject(ctx, d.providers.db, d.dep.ProjectID).ReleaseDomain
	hosts, _ := IntranetAddress(src.Slug, cfg.AppsDomain, vanity)
	if vanity != "" {
		logf("also answering on %s (DNS for it is platform-owned — needs its record applied)", vanity)
	}
	if secrets == nil {
		secrets = secret.Delivery{} // the app declares none
	}
	if givesSecrets(secrets, src.Contract) {
		logf("the app's secrets go as stack environment — Portainer keeps a copy with the stack definition")
	}
	prefix, base := portainer.StackNames(d.dep.ID)
	compose, err := d.providers.env.portainerStack(ctx, portainerStackOf{Base: base, Prefix: prefix, Hosts: hosts, Domain: cfg.AppsDomain, Contract: src.Contract, Revision: src.Revision,
		Images: stackImages{Parts: src.Images, Init: src.Init}, Secrets: secrets, Project: d.dep.ProjectID, Deployment: d.dep.ID}, logf)
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: "compose", Body: []byte(compose), Hash: deploy.ConfigHash(strings.TrimSpace(compose))}, nil
}

// Reconcile runs the stack at the address its hosts route: right for a
// first release, which has none recorded, and after the apps domain or the
// release domain changes.
func (d *portainerRelease) Reconcile(ctx context.Context, pl deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if art.Source != nil {
		cfg, err := d.Config(ctx, d.TargetID)
		if err != nil {
			return deploy.Observation{}, deploy.Terminal(err)
		}
		_, pl.URL = IntranetAddress(art.Source.Slug, cfg.AppsDomain, releaseProject(ctx, d.providers.db, d.dep.ProjectID).ReleaseDomain)
	}
	o, err := d.Driver.Reconcile(ctx, pl, generation, art, key, logf, checkpoint)
	if err == nil && o.Rollout == "active" && d.providers.afterRelease != nil {
		d.providers.afterRelease(d.dep.ProjectID, d.dep.Name)
	}
	return o, err
}

var _ deploy.Renderer = (*portainerRelease)(nil)

// notPublished refuses a release to the Portainer target named target whose
// images are not in the project's container registry by digest: Portainer
// pulls every image from there, and only a landing builds and publishes
// them. Both doors to Portainer, a deployment released by name and the
// intranet release, say it in these words.
func notPublished(target string) error {
	return refused("%s pulls every image from the project's container registry, and this release was not published there. Attach a container registry to the project, then land a change", target)
}

// givesSecrets is whether any part or init step of c is given a secret.
func givesSecrets(secrets deploy.ConsumerSecrets, c *appcontract.Contract) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Parts {
		if len(secrets.EnvFor(p.Name)) > 0 {
			return true
		}
	}
	for _, it := range c.Init {
		if len(secrets.EnvFor("init-"+it.Name)) > 0 {
			return true
		}
	}
	return false
}
