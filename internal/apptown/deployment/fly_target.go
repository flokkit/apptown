package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"maps"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/fly"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/strs"
)

// A Fly target is one Fly org and region, reached with an org token named by
// a cloud-account connector of provider "fly" (deploy-targets.md, "Fly
// Machines"). The connector and the target are made together by attaching;
// the token is resolved from AppTown's own secrets at each use, exactly as
// App Platform's is, and never stored.

// flyProvider is the connector provider name of a Fly account.
const flyProvider = "fly"

// flyConfig reads a Fly connector's current settings, without the token.
func (p *Providers) flyConfig(ctx context.Context, provider string) (fly.Config, error) {
	cfg, err := p.integrations.ConnectorConfig(ctx, provider, digitalocean.CapCloudAccount)
	if err != nil {
		return fly.Config{}, err
	}
	if cfg == nil {
		return fly.Config{}, fmt.Errorf("no cloud-account connector named %q", provider)
	}
	return flyConfiguration(cfg), nil
}

func flyConfiguration(cfg map[string]any) fly.Config {
	str := func(k, def string) string {
		if v, _ := cfg[k].(string); v != "" {
			return v
		}
		return def
	}
	return fly.Config{TokenFrom: str("tokenFrom", "FLY_API_TOKEN"), Org: str("org", ""), Region: str("region", "")}
}

// flyClient resolves the named token for this operation.
func (p *Providers) flyClient(ctx context.Context, cfg fly.Config) (*fly.Client, error) {
	values, err := p.secrets.WithContext(ctx)
	if err != nil {
		return nil, err
	}
	tok := values[cfg.TokenFrom]
	if tok == "" {
		return nil, fmt.Errorf("the Fly token %s does not resolve — add it to the secrets store", cfg.TokenFrom)
	}
	return fly.NewClient(tok, p.fly.APIBase), nil
}

// flyClientFor resolves a Fly connector's token and settings at use time.
func (p *Providers) flyClientFor(provider string) func(context.Context) (*fly.Client, fly.Config, error) {
	return func(ctx context.Context) (*fly.Client, fly.Config, error) {
		if err := ctx.Err(); err != nil {
			return nil, fly.Config{}, err
		}
		cfg, err := p.flyConfig(ctx, provider)
		if err != nil {
			return nil, cfg, deploy.Terminal(err)
		}
		client, err := p.flyClient(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cfg, ctx.Err()
			}
			return nil, cfg, deploy.Terminal(err)
		}
		return client, cfg, nil
	}
}

func (p *Providers) newFlyDriver(ctx context.Context, t *Target, dep *Deployment) (*fly.Driver, error) {
	connection, err := p.integrations.Bindings.ByName(ctx, t.Provider)
	if err != nil {
		return nil, fmt.Errorf("reading deployment connection: %w", err)
	}
	if !t.ConnectionID.IsZero() && t.ConnectionID != connection.ID {
		return nil, fmt.Errorf("deployment connection identity changed")
	}
	h := holdings{deployments: p.deployments, dep: dep}
	return fly.NewDriver(fly.Dependencies{
		Client:       p.flyClientFor(t.Provider),
		Installation: p.installation,
		Owner:        h,
		Owned:        h.Owned,
		Needs: func(ctx context.Context, c *appcontract.Contract, hosts map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
			c, err := p.env.needs.ResolveSizes(ctx, c)
			if err != nil {
				return nil, nil, err
			}
			if why, err := p.env.needs.RefusesNeeds(ctx, c.Needs, c.NeedParams); err != nil {
				return nil, nil, err
			} else if why != "" {
				return nil, nil, fmt.Errorf("%s", why)
			}
			var services []deploy.ServiceSpec
			env := map[string]string{}
			for _, need := range c.Needs {
				k, err := p.env.needs.ByCapability(ctx, need)
				if err != nil {
					return nil, nil, err
				}
				generated, err := k.Generated(ctx, p.env.NeedCredentials(NeedSecretsScope(dep.ID)))
				if err != nil {
					return nil, nil, err
				}
				local := k.ResolveLocal(c.NeedParams[need], hosts[need], generated)
				if local == nil {
					continue
				}
				spec := local.Spec()
				spec.Resources = c.NeedResources[need]
				services = append(services, spec)
				maps.Copy(env, local.Inject)
			}
			return services, env, nil
		},
		ReleaseLogin: func(ref string) (oci.Credential, error) { return p.releaseCredential(ctx, dep.ProjectID, ref) },
	}, p.fly), nil
}

// CheckFlyAccount is a Fly connector's health check: what its token reaches,
// and whether its org and region answer.
func (p *Providers) CheckFlyAccount(ctx context.Context, config map[string]any) ProviderHealth {
	ok, detail := p.probeFly(ctx, flyConfiguration(config))
	return ProviderHealth{OK: ok, Detail: detail}
}

// checkFly asks Fly what the target's token reaches and whether its org and
// region still answer.
func (p *Providers) checkFly(ctx context.Context, t *Target) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg, err := p.flyConfig(ctx, t.Provider)
	if err != nil {
		return false, "the Fly account is not bound: " + strs.Truncate(err.Error(), 160)
	}
	return p.probeFly(ctx, cfg)
}

// probeFly reads what the token reaches and checks the org and region. It
// changes nothing.
func (p *Providers) probeFly(ctx context.Context, cfg fly.Config) (bool, string) {
	c, err := p.flyClient(ctx, cfg)
	if err != nil {
		return false, err.Error()
	}
	acc, err := fly.ReadAccount(ctx, c)
	if err != nil {
		return false, "Fly did not answer: " + strs.Truncate(err.Error(), 160)
	}
	org, err := acc.ChooseOrg(cfg.Org)
	if err != nil {
		return false, err.Error()
	}
	region, err := acc.Region(cfg.Region)
	if err != nil {
		return false, fmt.Sprintf("%v; choose one of %s", err, regionWords(acc))
	}
	return true, fmt.Sprintf("Fly answers: org %s, region %s (%s). This token can manage every app in the org %s, including ones AppTown did not make; AppTown touches only what it made and recorded", org, region.Code, region.Name, org)
}

func regionWords(acc fly.Account) string {
	var codes []string
	for _, r := range acc.Regions {
		codes = append(codes, r.Code)
	}
	return strings.Join(codes, ", ")
}

// attachFly checks a Fly account and, unless it is a dry run, records its
// connector and target together. The org is the token's own answer, or the
// one named among several it reaches.
func (s *Service) attachFly(ctx context.Context, in TargetAttachment, by string) (targetAttachmentResult, error) {
	in.TokenFrom, in.Region, in.Org = strings.TrimSpace(in.TokenFrom), strings.TrimSpace(in.Region), strings.TrimSpace(in.Org)
	if in.TokenFrom == "" || in.Region == "" {
		return targetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("a Fly target needs tokenFrom (the secret holding an org token) and a region"))
	}
	cfg := fly.Config{TokenFrom: in.TokenFrom, Org: in.Org, Region: in.Region}
	c, err := s.providers.flyClient(ctx, cfg)
	if err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %v", err))
	}
	acc, err := fly.ReadAccount(ctx, c)
	if err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %v", err))
	}
	org, err := acc.ChooseOrg(in.Org)
	if err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %v", err))
	}
	cfg.Org = org
	ok, detail := s.providers.probeFly(ctx, cfg)
	if !ok {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %s", detail))
	}
	if in.DryRun {
		return targetAttachmentResult{Detail: detail}, nil
	}
	config := map[string]any{"tokenFrom": cfg.TokenFrom, "org": cfg.Org, "region": cfg.Region}
	saved, err := s.attachFlyTarget(ctx, in.Name, config, attachment{By: by,
		Attached: map[string]any{"kind": kindFlyMachines, "org": cfg.Org, "region": cfg.Region, "tokenFrom": cfg.TokenFrom, "answered": detail},
		Answer:   map[string]any{"ok": true, "detail": detail}})
	if err != nil {
		return targetAttachmentResult{}, err
	}
	return targetAttachmentResult{Target: saved, Detail: detail}, nil
}

// attachFlyTarget creates a Fly account's connector and its target together.
func (s *Service) attachFlyTarget(ctx context.Context, account string, config map[string]any, at attachment) (*Target, error) {
	raw, err := json.Marshal(maps.Clone(config))
	if err != nil {
		return nil, fault.Wrap(fault.Invalid, err)
	}
	target := flyTarget(account, flyConfiguration(config))
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		if err := deploymentsql.New(tx.SQL()).LockTargets(ctx); err != nil {
			return nil, err
		}
		saved, err := s.Targets.attachIn(ctx, tx, target, at)
		if err != nil {
			return nil, err
		}
		if err := s.providers.integrations.Bindings.CreateIn(ctx, tx, account, digitalocean.CapCloudAccount, flyProvider, string(raw), at.By); err != nil {
			return nil, err
		}
		return saved, nil
	})
}

// seedFlyTarget records the target a Fly account offers, from its
// connector; settings an administrator saved on the target are kept.
func (s *Service) seedFlyTarget(ctx context.Context, account, who string) (*Target, error) {
	cfg, err := s.providers.flyConfig(ctx, account)
	if err != nil {
		return nil, err
	}
	return s.Targets.sync(ctx, flyTarget(account, cfg), who)
}

func flyTarget(account string, cfg fly.Config) *Target {
	return &Target{
		Name: account, Kind: kindFlyMachines, Provider: account, Origin: OriginAttached,
		Address:      map[string]any{"org": cfg.Org, "region": cfg.Region},
		Capabilities: stockCapabilities(kindFlyMachines, nil),
		Ingress:      map[string]any{"binding": "provider-issued", "domain": "fly.dev"},
		NeedsMode:    stockNeedsMode(kindFlyMachines), Status: TargetReady,
	}
}

// flyInventory reads a Fly target's org.
func (p *Providers) flyInventory(t *Target) (deploy.Inventorier, error) {
	return fly.Inventory{Client: p.flyClientFor(t.Provider), Installation: p.installation}, nil
}

// FlyData reaches one deployment's volumes and catalogue containers through
// the same account and owned list as its release driver.
func (p *Providers) FlyData(ctx context.Context, t *Target, dep *Deployment) (*fly.Driver, error) {
	return p.newFlyDriver(ctx, t, dep)
}
