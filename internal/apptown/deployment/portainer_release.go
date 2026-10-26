package deployment

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

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
	if bundle == nil || !bundle.ByDigest() {
		return notPublished(d.target.Name)
	}
	return nil
}

func (d *portainerRelease) Render(ctx context.Context, pl deploy.Placement, src deploy.Source, _ deploy.NeedBindings, secrets deploy.ConsumerSecrets, logf deploy.Logf) (deploy.Artifact, error) {
	if src.Contract == nil || src.Contract.PublicPart() == nil {
		return deploy.Artifact{}, refused("Portainer readiness requires an admitted public part")
	}
	pub := src.Contract.PublicPart()
	if err := portainer.RefusesHealth(src.Contract); err != nil {
		return deploy.Artifact{}, refused(err.Error())
	}
	policy, err := portainer.PublicHealthPolicy(src.Contract)
	if err != nil {
		return deploy.Artifact{}, err
	}
	if !src.Published {
		return deploy.Artifact{}, notPublished(d.target.Name)
	}
	if src.Ingress == nil {
		return deploy.Artifact{}, refused("release has no admitted deployment address")
	}
	if err := (AddressSnapshot{DeploymentID: d.dep.ID, TargetID: d.target.ID, Revision: 1, Ingress: *src.Ingress}).valid(); err != nil {
		return deploy.Artifact{}, err
	}
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return deploy.Artifact{}, err
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
	hosts := src.Ingress.Hosts
	if len(hosts) > 1 {
		logf("also answering on %s (DNS for it is platform-owned — needs its record applied)", hosts[1])
	}
	if secrets == nil {
		secrets = secret.Delivery{} // the app declares none
	}
	if givesSecrets(secrets, src.Contract) {
		logf("the app's secrets go as stack environment — Portainer keeps a copy with the stack definition")
	}
	prefix, base := portainer.StackNames(d.dep.ID)
	compose, err := d.providers.env.portainerStack(ctx, portainerStackOf{Base: base, Prefix: prefix, Hosts: hosts, Domain: src.Ingress.CertificateDomain, Contract: src.Contract, Revision: src.Revision,
		Images: stackImages{Parts: src.Images, Init: src.Init}, Secrets: secrets, Project: d.dep.ProjectID, Deployment: d.dep.ID}, logf)
	if err != nil {
		return deploy.Artifact{}, err
	}
	origin, err := url.Parse(src.Ingress.URL)
	if err != nil || origin.Host == "" {
		return deploy.Artifact{}, fmt.Errorf("Portainer readiness has no admitted origin")
	}
	probe := docker.RenderColdBootProbe(pub, map[string]string{"Host": origin.Host, "X-Forwarded-Host": origin.Host, "X-Forwarded-Proto": origin.Scheme})
	ready := portainer.Readiness{URL: src.Ingress.URL, Version: 1, Deployment: d.dep.ID, Target: d.target.ID, Revision: src.Revision,
		Public: portainer.Probe{ToolImage: d.providers.env.ToolVersions().Images.DF, Port: pub.Port, Path: probe.Path, Headers: probe.Headers, Expect: probe.Expect, GraceNanos: int64(probe.Grace), StaticRelease: src.Contract.PublicRelease(strings.TrimSpace(src.Revision))}}
	if policy.Explicit {
		ready.Version = 2
		ready.Public.Policy = &policy
	}
	for _, part := range src.Contract.Parts {
		service := part.Name
		if part.Public {
			service = "app"
		}
		ready.Components = append(ready.Components, portainer.Component{Service: service, Image: src.Images[part.Name], ShellHealth: !part.Public && part.Health != ""})
	}
	for _, init := range src.Contract.Init {
		ready.Components = append(ready.Components, portainer.Component{Service: "init-" + init.Name, Image: src.Init[init.Name], Init: true})
	}
	compose, err = portainer.WithReadiness(compose, ready)
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: "compose", Body: []byte(compose), Hash: deploy.ConfigHash(strings.TrimSpace(compose))}, nil
}

// Reconcile uses the immutable address carried by the admitted artifact.
func (d *portainerRelease) Reconcile(ctx context.Context, pl deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if art.Source == nil || art.Source.Ingress == nil {
		return deploy.Observation{}, deploy.Terminal(refused("release has no admitted deployment address"))
	}
	if err := (AddressSnapshot{DeploymentID: d.dep.ID, TargetID: d.target.ID, Revision: 1, Ingress: *art.Source.Ingress}).valid(); err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	pl.URL = art.Source.Ingress.URL
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
