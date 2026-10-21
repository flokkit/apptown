package deployment

import (
	"context"
	"fmt"
	"log"
	"time"

	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/deploy/s3bucket"
	"flokkit.io/forge/internal/strs"
)

type ProviderHealth struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// BuiltInTargetName is the built-in Docker host's target.
const BuiltInTargetName = "built-in-docker"

// SeedTargets names what exists (design/subsystems/deployment.md §8, §12A):
// the built-in Docker host, unless this AppTown has no local Docker;
// each DigitalOcean account's App Platform target; and the intranet's
// Portainer environment when the portainer-deploy binding resolves. Existing
// deployments keep their explicit targets. ingressDomains are the built-in
// host's tier domains, as tier=domain.
//
// A target is a reusable place AppTown can deploy to, reached through one
// provider. The test for whether something is a target: does it exist as an
// offered place BEFORE any environment is bound to it? A Docker daemon, a
// Portainer environment, an App Platform team and region are targets; one
// app on App Platform is an environment's handle, never a target. A fresh
// AppTown has exactly one target, the built-in Docker host; everything else is
// attached by an administrator.
//
// Each is recorded through Targets.Sync, so a start refreshes only the facts
// configuration owns and keeps what an administrator set; a change it makes
// is receipted to AppTown itself.
func (s *Service) SeedTargets(ctx context.Context, ingressDomains []string) {
	const who = "forge"
	if !s.hosts.settings.NoLocalDocker { // with no local daemon there is no built-in host
		_, err := s.Targets.Sync(ctx, &Target{
			Name: s.hosts.settings.BuiltInTarget, Kind: KindDockerHost, Provider: "", Origin: OriginBuiltIn,
			Address:      map[string]any{"daemon": "local"},
			Capabilities: StockCapabilities(KindDockerHost, s.env.needs.EmulatorKinds(ctx)),
			Ingress:      map[string]any{"binding": "apptown-vhost", "domains": ingressDomains},
			NeedsMode:    stockNeedsMode(KindDockerHost), SecretsDelivery: "inject", Status: TargetReady,
		}, who)
		if err != nil {
			log.Printf("targets: seeding the built-in Docker host: %v", err)
			return
		}
	}
	s.SeedCloudTargets(ctx, who)
	if cfg, err := s.env.integrations.PortainerTarget(ctx); err == nil {
		if _, err := s.Targets.Sync(ctx, &Target{
			Name: IntranetTargetName, Kind: KindPortainer, Provider: "portainer-deploy", Origin: OriginAttached,
			Address:      map[string]any{"url": cfg.PortainerURL, "environment": cfg.EnvName, "tier": cfg.ReleaseDeployment},
			Capabilities: StockCapabilities(KindPortainer, s.env.needs.EmulatorKinds(ctx)),
			Ingress:      map[string]any{"binding": "traefik", "domain": cfg.AppsDomain},
			NeedsMode:    stockNeedsMode(KindPortainer), SecretsDelivery: "inject", Status: TargetReady,
		}, who); err != nil {
			log.Printf("targets: recording the intranet target: %v", err)
		}
	}
}

// StockCapabilities is what a kind of target offers as AppTown records it
// (deploy-targets.md, "What each target supports"); emulators are the need
// kinds this AppTown can run locally. An administrator may later change
// PersistentVolumes and Previews on a target (target_settings.go).
func StockCapabilities(kind string, emulators []string) TargetCapabilities {
	switch kind {
	case KindDockerHost:
		return TargetCapabilities{PersistentVolumes: true, Previews: true, OwnLiveness: false, Rollout: []string{"replace"}, Needs: emulators}
	case KindPortainer:
		return TargetCapabilities{PersistentVolumes: true, Previews: false, OwnLiveness: true, Rollout: []string{"replace"}, Needs: emulators}
	case KindDOApp:
		return TargetCapabilities{PersistentVolumes: false, Previews: false, OwnLiveness: true, Rollout: []string{"rolling", "rollback"}, Needs: []string{"postgres", "s3"}}
	}
	return TargetCapabilities{}
}

// stockNeedsMode is how a kind of target fulfils an app's needs: local
// emulators, or managed services from the provider.
func stockNeedsMode(kind string) string {
	switch kind {
	case KindDockerHost, KindPortainer:
		return "emulators"
	case KindDOApp:
		return "managed"
	}
	return ""
}

// SeedCloudTargets: every DigitalOcean cloud-account connector offers one
// App Platform target in its region (§2.2: a team and region is a place
// that exists before any environment binds to it). who is whom a change is
// receipted to: AppTown at a start, the person saving a connector.
func (s *Service) SeedCloudTargets(ctx context.Context, who string) {
	names, err := s.env.integrations.Bindings.NamesByProvider(ctx, digitalocean.CapCloudAccount, "digitalocean")
	if err != nil {
		log.Printf("seeding cloud targets: %v", err)
		return
	}
	// Best effort per connector: the next start or connector save seeds again.
	for _, n := range names {
		if _, err := s.SeedCloudTarget(ctx, n, who); err != nil {
			log.Printf("seeding the cloud target for %s: %v", n, err)
		}
	}
}

// SeedCloudTarget records the App Platform target a DigitalOcean account
// offers, from the account's connector config. Idempotent. The connector
// owns the region, registry and sizes; the copy runner, domain and other
// settings an administrator edits are kept (Targets.Sync).
func (s *Service) SeedCloudTarget(ctx context.Context, account, who string) (*Target, error) {
	cfg, err := s.providers.DigitalOceanConfig(ctx, account)
	if err != nil {
		return nil, err
	}
	return s.Targets.Sync(ctx, &Target{
		Name: account, Kind: KindDOApp, Provider: account, Origin: OriginAttached,
		Address:      map[string]any{"region": cfg.Region, "registry": cfg.Registry, "instanceSize": cfg.InstanceSize, "dbSize": cfg.DBSize},
		Capabilities: StockCapabilities(KindDOApp, nil),
		Ingress:      map[string]any{"binding": "provider-issued", "domain": "ondigitalocean.app"},
		NeedsMode:    stockNeedsMode(KindDOApp), SecretsDelivery: "inject", Status: TargetReady,
	}, who)
}

// RegistryReading is the container registry of an App Platform target's
// account as DigitalOcean answered: its plan (subscription tier), the
// repositories it includes (0: no limit) and how many it holds. Each
// project released to App Platform takes one, and AppTown's gate image
// one more (Gate: it is among those used). None says the account has no
// registry yet; Unknown says why it could not be read. ReadAt is when, for
// a reading recorded by a check.
type RegistryReading struct {
	Registry string `json:"registry,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Included int    `json:"included"`
	Used     int    `json:"used"`
	Full     bool   `json:"full"`
	Gate     bool   `json:"gate,omitempty"`
	None     bool   `json:"none,omitempty"`
	Unknown  string `json:"unknown,omitempty"`
	ReadAt   string `json:"readAt,omitempty"`
}

// Words say the reading in a sentence.
func (r RegistryReading) Words() string {
	switch {
	case r.Unknown != "":
		return "container registry unknown: " + r.Unknown
	case r.None:
		return "no container registry yet"
	}
	room := digitalocean.RegistryRoom{Tier: r.Tier, Included: r.Included, Repositories: make([]string, r.Used)}
	words := "container registry " + r.Registry + ": " + room.String()
	if r.Gate {
		words += " (one holds AppTown's gate image, " + digitalocean.GateRepository + ")"
	}
	if r.Full {
		words += "; registry plan full — a project that has not released here before cannot until the plan is upgraded or a deployment is taken down"
	}
	return words
}

// readRegistry asks DigitalOcean for the account's registry and its room.
func readRegistry(ctx context.Context, c *digitalocean.Client) RegistryReading {
	name, err := c.RegistryName(ctx)
	if err != nil {
		return RegistryReading{Unknown: strs.Truncate(err.Error(), 160)}
	}
	if name == "" {
		return RegistryReading{None: true}
	}
	r, err := c.RegistryRoom(ctx, name)
	if err != nil {
		return RegistryReading{Registry: name, Unknown: strs.Truncate(err.Error(), 160)}
	}
	return RegistryReading{Registry: name, Tier: r.Tier, Included: r.Included, Used: len(r.Repositories), Full: r.Full(), Gate: r.Holds(digitalocean.GateRepository)}
}

// DigitalOceanRegistry reads the account's container registry: one per
// account, created on the first release under the name the connector gives.
func (p *Providers) DigitalOceanRegistry(ctx context.Context, cfg digitalocean.Config) (RegistryReading, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := p.DigitalOceanClient(ctx, cfg)
	if err != nil {
		return RegistryReading{}, err
	}
	return readRegistry(ctx, c), nil
}

// TargetCheck is a target's answer to a check: whether it answered, in
// words, and for an App Platform target its registry's reading.
type TargetCheck struct {
	OK       bool
	Detail   string
	Registry *RegistryReading
}

// CheckTarget asks a target whether it answers: the daemon for a Docker
// host, the API for Portainer and a cloud account. A kind with no check
// yet says so rather than pretending.
func (p *Providers) CheckTarget(ctx context.Context, t *Target) TargetCheck {
	if t.Kind == KindDOApp {
		return p.checkAppPlatform(ctx, t)
	}
	ok, detail := p.checkTarget(ctx, t)
	return TargetCheck{OK: ok, Detail: detail}
}

func (p *Providers) checkTarget(ctx context.Context, t *Target) (ok bool, detail string) {
	switch t.Kind {
	case KindDockerHost:
		if t.Origin == OriginBuiltIn {
			if v := p.hosts.settings.Scope.Text(ctx, "info", "--format", "{{.ServerVersion}} {{.ContainersRunning}}/{{.Containers}} containers"); v != "" {
				return true, "docker " + v
			}
			return false, "the Docker daemon did not answer"
		}
		addr, keyFrom, err := HostTargetAddress(t)
		if err != nil {
			return false, err.Error()
		}
		return p.hosts.ProbeHostTarget(ctx, t.Name, addr, keyFrom)
	case KindPortainer:
		cfg, err := p.PortainerConfig(ctx, t)
		if err != nil {
			return false, "Portainer is not bound: " + strs.Truncate(err.Error(), 160)
		}
		id, err := portainer.EndpointID(ctx, cfg)
		if err != nil {
			return false, "Portainer did not answer: " + strs.Truncate(err.Error(), 160)
		}
		return true, fmt.Sprintf("Portainer answers; endpoint %d (%s)", id, portainer.PlatformText(ctx, cfg, id))
	case KindS3Bucket:
		return p.CheckS3Bucket(ctx, S3BucketConfig(t))
	}
	return false, "no check yet for a " + t.Kind + " target"
}

// checkAppPlatform asks DigitalOcean for the account and reads its
// container registry.
func (p *Providers) checkAppPlatform(ctx context.Context, t *Target) TargetCheck {
	cfg, err := p.DigitalOceanConfig(ctx, t.Provider)
	if err != nil {
		return TargetCheck{Detail: "the cloud account is not bound: " + strs.Truncate(err.Error(), 160)}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := p.DigitalOceanClient(ctx, cfg)
	if err != nil {
		return TargetCheck{Detail: strs.Truncate(err.Error(), 160)}
	}
	var acct struct {
		Account struct {
			Email  string `json:"email"`
			Status string `json:"status"`
		} `json:"account"`
	}
	if err := c.Call(ctx, "GET", "/v2/account", nil, &acct); err != nil {
		return TargetCheck{Detail: "DigitalOcean did not answer: " + strs.Truncate(err.Error(), 160)}
	}
	reg := readRegistry(ctx, c)
	return TargetCheck{OK: true, Detail: fmt.Sprintf("DigitalOcean answers for %s (%s); %s", acct.Account.Email, acct.Account.Status, reg.Words()), Registry: &reg}
}

// S3BucketWords is what attaching and checking a bucket target say about
// the key and the buckets, every time, in these words.
const S3BucketWords = "AppTown creates one bucket per deployment, so this key must be able to create buckets: a full-access key, which can read, write and configure every bucket in its team (not Droplets or the control panel). Use a key made for AppTown alone, and a team of its own where other data must be out of its reach. Don't store anything else in a deployment's bucket: taking the deployment down deletes it"

// CheckS3Bucket asks the store whether the key pair is accepted, by
// listing the account's buckets.
func (p *Providers) CheckS3Bucket(ctx context.Context, cfg s3bucket.Config) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := p.S3BucketClient(ctx, cfg)
	if err != nil {
		return false, strs.Truncate(err.Error(), 200)
	}
	names, err := c.ListBuckets(ctx)
	if err != nil {
		return false, "the store did not accept the key pair: " + strs.Truncate(err.Error(), 200)
	}
	return true, fmt.Sprintf("%s answers in %s; the key sees %d buckets. %s", cfg.Endpoint, cfg.Region, len(names), S3BucketWords)
}

// CheckCloudAccount is the connector's health check: the account call.
func (p *Providers) CheckCloudAccount(ctx context.Context, config map[string]any) ProviderHealth {
	tokenFrom, _ := config["tokenFrom"].(string)
	if tokenFrom == "" {
		tokenFrom = "DIGITALOCEAN_ACCESS_TOKEN"
	}
	c, err := p.DigitalOceanClient(ctx, digitalocean.Config{TokenFrom: tokenFrom})
	if err != nil {
		return ProviderHealth{OK: false, Detail: err.Error()}
	}
	var acct struct {
		Account struct {
			Email  string `json:"email"`
			Status string `json:"status"`
			Team   struct {
				Name string `json:"name"`
			} `json:"team"`
		} `json:"account"`
	}
	if err := c.Call(ctx, "GET", "/v2/account", nil, &acct); err != nil {
		return ProviderHealth{OK: false, Detail: err.Error()}
	}
	return ProviderHealth{OK: true, Detail: fmt.Sprintf("account %s (%s) on team %s", acct.Account.Email, acct.Account.Status, acct.Account.Team.Name)}
}
