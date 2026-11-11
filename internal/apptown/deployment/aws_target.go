package deployment

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/oci"
)

func awsConfiguration(cfg map[string]any) awsecs.Config {
	str := func(k, def string) string {
		if v, _ := cfg[k].(string); v != "" {
			return v
		}
		return def
	}
	list := func(k string) []string {
		switch v := cfg[k].(type) {
		case []string:
			return v
		case []any:
			var out []string
			for _, s := range v {
				if t, ok := s.(string); ok {
					out = append(out, t)
				}
			}
			return out
		}
		return nil
	}
	return awsecs.Config{KeyIDFrom: str("keyIdFrom", "AWS_ACCESS_KEY_ID"), SecretKeyFrom: str("secretKeyFrom", "AWS_SECRET_ACCESS_KEY"), Region: str("region", ""), Account: str("account", ""), Cluster: str("cluster", ""), Listener: str("listener", ""), ExecutionRole: str("executionRole", ""), Domain: str("domain", ""), TaskSubnets: list("taskSubnets"), DatabaseSubnets: list("databaseSubnets")}
}
func (p *Providers) awsClient(ctx context.Context, c awsecs.Config) (*awsecs.API, error) {
	values, err := p.secrets.WithContext(ctx)
	if err != nil {
		return nil, err
	}
	key, secret := values[c.KeyIDFrom], values[c.SecretKeyFrom]
	if key == "" || secret == "" {
		return nil, fmt.Errorf("AWS credentials %s and %s must resolve in the secrets store", c.KeyIDFrom, c.SecretKeyFrom)
	}
	if p.aws.API != nil {
		return p.aws.API(c, key, secret)
	}
	return awsecs.NewAPI(c.Region, key, secret)
}
func (p *Providers) awsClientFor(t *Target) func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
	return func(ctx context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		binding, err := p.integrations.Bindings.ByName(ctx, t.Provider)
		if err != nil {
			return nil, awsecs.Config{}, awsecs.Estate{}, err
		}
		if !t.ConnectionID.IsZero() && t.ConnectionID != binding.ID {
			return nil, awsecs.Config{}, awsecs.Estate{}, errors.New("AWS account connection identity changed")
		}
		cfg, err := p.integrations.ConnectorConfigByID(ctx, binding.ID, digitalocean.CapCloudAccount)
		if err != nil {
			return nil, awsecs.Config{}, awsecs.Estate{}, err
		}
		c := awsConfiguration(cfg)
		c.Domain, _ = t.Ingress["domain"].(string)
		a, err := p.awsClient(ctx, c)
		if err != nil {
			return nil, c, awsecs.Estate{}, err
		}
		e, err := awsecs.CheckAccount(ctx, a, c, p.installation)
		return a, c, e, err
	}
}
func (p *Providers) newAWSDriver(ctx context.Context, t *Target, dep *Deployment) (*awsecs.Driver, error) {

	h := holdings{deployments: p.deployments, dep: dep}
	d := awsecs.NewDriver(awsecs.Dependencies{Client: p.awsClientFor(t), Installation: p.installation, Owner: h, Owned: h.Owned, Needs: p.needs, Now: p.clock})
	if p.env.needs != nil {
		kind, err := p.env.needs.ByCapability(ctx, "postgres")
		if err != nil {
			return nil, err
		}
		d.PostgresSetup = kind.Cloud["aws"].Setup
	}
	d.Credentials = func(ctx context.Context) (map[string]string, error) {
		store := p.needSecrets(dep)
		if store == nil {
			return nil, errors.New("AWS RDS needs the built-in secrets store")
		}
		out := map[string]string{}
		for _, name := range []string{"password", "adminPassword"} {
			key := "aws.postgres." + name
			value, err := store.Get(ctx, key)
			if err != nil {
				return nil, err
			}
			if value == "" {
				b := make([]byte, 32)
				if _, err = rand.Read(b); err != nil {
					return nil, err
				}
				value = hex.EncodeToString(b)
				if err = store.Put(ctx, key, value); err != nil {
					return nil, err
				}
			}
			out[name] = value
		}
		return out, nil
	}
	d.CopyImage = func(ctx context.Context, placement deploy.Placement, ref string) (string, error) {
		a, c, _, err := d.Client(ctx)
		if err != nil {
			return "", err
		}
		owned, err := h.Owned(ctx)
		if err != nil {
			return "", err
		}
		repo := ""
		for _, o := range owned {
			if o.Kind == "ecr-repository" {
				repo = c.Account + ".dkr.ecr." + c.Region + ".amazonaws.com/" + o.Name
			}
		}
		if repo == "" {
			return "", errors.New("AWS has no recorded image repository")
		}
		if p.aws.CopyImage != nil {
			return p.aws.CopyImage(ctx, repo, ref)
		}
		tokens, err := a.ECR.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
		if err != nil {
			return "", err
		}
		if len(tokens.AuthorizationData) != 1 {
			return "", errors.New("AWS returned no registry credential")
		}
		decoded, err := base64.StdEncoding.DecodeString(aws.ToString(tokens.AuthorizationData[0].AuthorizationToken))
		if err != nil {
			return "", errors.New("AWS returned a malformed registry credential")
		}
		user, password, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return "", errors.New("AWS returned a malformed registry credential")
		}
		from, err := p.releaseCredential(ctx, dep.ProjectID, ref)
		if err != nil {
			return "", err
		}
		image, err := oci.Copy(ctx, oci.Source{Reference: ref, Credential: from}, oci.Repository{Name: repo, Credential: oci.Credential{Username: user, Password: password}}, "copy-"+deploy.ConfigHash(ref)[:16], awsecs.Platform)
		if err != nil {
			return "", errors.New(strings.ReplaceAll(err.Error(), password, "<credential>"))
		}
		if _, want, ok := strings.Cut(ref, "@"); ok && image.Digest != want {
			return "", errors.New("AWS registry changed the release image digest")
		}
		return repo + "@" + image.Digest, nil
	}
	return d, nil
}
func (p *Providers) AWSData(ctx context.Context, t *Target, dep *Deployment) (*awsecs.Driver, error) {
	return p.newAWSDriver(ctx, t, dep)
}
func (p *Providers) awsManaged(t *Target) awsecs.ManagedData {
	return awsecs.ManagedData{Resolve: func(ctx context.Context, instance string) (*awsecs.Driver, deploy.Placement, error) {
		ds, err := p.deployments.OnTarget(ctx, t.ID)
		if err != nil {
			return nil, deploy.Placement{}, err
		}
		for _, dep := range ds {
			needs, err := p.needs.ForDeployment(ctx, dep.ID)
			if err != nil {
				return nil, deploy.Placement{}, err
			}
			for _, n := range needs {
				if textValue(n.Handle, "id") == instance {
					d, err := p.newAWSDriver(ctx, t, &dep.Deployment)
					return d, dep.Placement(), err
				}
			}
		}
		return nil, deploy.Placement{}, errors.New("RDS instance is not recorded on this target")
	}}
}
func textValue(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func (s *Service) attachAWS(ctx context.Context, in TargetAttachment, by string) (targetAttachmentResult, error) {
	cfg := map[string]any{"keyIdFrom": in.KeyIDFrom, "secretKeyFrom": in.SecretKeyFrom, "region": in.Region, "account": in.Account, "cluster": in.Cluster, "listener": in.Listener, "executionRole": in.ExecutionRole, "taskSubnets": in.TaskSubnets, "databaseSubnets": in.DatabaseSubnets, "domain": in.Domain}
	c := awsConfiguration(cfg)
	domain, err := normalizeTargetDomain(c.Domain)
	if err != nil || domain == "" {
		return targetAttachmentResult{}, fault.New(fault.Invalid, "AWS needs a valid apps domain")
	}
	c.Domain = domain
	cfg["domain"] = domain
	if err := c.Validate(); err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Invalid, err)
	}
	a, err := s.providers.awsClient(ctx, c)
	if err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, err)
	}
	e, err := awsecs.CheckAccount(ctx, a, c, s.providers.installation)
	if err != nil {
		return targetAttachmentResult{}, fault.Wrap(fault.Unprocessable, err)
	}
	detail := fmt.Sprintf("This key acts in account %s in %s. AppTown cannot see whether other policies are attached to its user. Load balancer: %s. Point *.%s to it.", c.Account, c.Region, e.LoadBalancerDNS, c.Domain)
	if in.DryRun {
		return targetAttachmentResult{Detail: detail}, nil
	}
	target := &Target{Name: in.Name, Kind: KindAWSECS, Provider: in.Name, Origin: OriginAttached, Address: maps.Clone(cfg), Capabilities: stockCapabilities(KindAWSECS, nil), Ingress: map[string]any{"binding": "provider-issued", "domain": c.Domain, "loadBalancerDNS": e.LoadBalancerDNS}, NeedsMode: "managed", Status: TargetReady}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return targetAttachmentResult{}, err
	}
	saved, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		if err := deploymentsql.New(tx.SQL()).LockTargets(ctx); err != nil {
			return nil, err
		}
		saved, err := s.Targets.attachIn(ctx, tx, target, attachment{By: by, Attached: cfg, Answer: map[string]any{"ok": true, "detail": detail}})
		if err != nil {
			return nil, err
		}
		if err = s.providers.integrations.Bindings.CreateIn(ctx, tx, in.Name, digitalocean.CapCloudAccount, "aws", string(raw), by); err != nil {
			return nil, err
		}
		return saved, nil
	})
	return targetAttachmentResult{Target: saved, Detail: detail}, err
}

func (p *Providers) CheckAWSAccount(ctx context.Context, config map[string]any) ProviderHealth {
	c := awsConfiguration(config)
	a, err := p.awsClient(ctx, c)
	if err != nil {
		return ProviderHealth{Detail: err.Error()}
	}
	e, err := awsecs.CheckAccount(ctx, a, c, p.installation)
	if err != nil {
		return ProviderHealth{Detail: err.Error()}
	}
	return ProviderHealth{OK: true, Detail: "AWS answers; load balancer " + e.LoadBalancerDNS}
}

// AWSRoutingReading keeps a failed DNS check distinct from a successful answer.
type AWSRoutingReading struct {
	LoadBalancer string    `json:"loadBalancer"`
	Domain       string    `json:"domain"`
	Matches      bool      `json:"matches"`
	Unknown      string    `json:"unknown,omitempty"`
	ReadAt       time.Time `json:"readAt"`
}

// AWSRouting checks the target's wildcard address against its attached load
// balancer. It reads DNS only; it never changes records or calls AWS.
func (s *Service) AWSRouting(ctx context.Context, t *Target) *AWSRoutingReading {
	if textValue(t.Ingress, "loadBalancerDNS") == "" {
		return nil
	}
	r := &AWSRoutingReading{LoadBalancer: textValue(t.Ingress, "loadBalancerDNS"), Domain: textValue(t.Ingress, "domain"), ReadAt: s.now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, "apptown-check-"+deploy.HostKey(t.ID)+"."+r.Domain)
	if err != nil {
		r.Unknown = "The wildcard apps address could not be read: " + err.Error()
		return r
	}
	lb, err := net.DefaultResolver.LookupHost(ctx, r.LoadBalancer)
	if err != nil {
		r.Unknown = "The load balancer address could not be read: " + err.Error()
		return r
	}
	for _, a := range ips {
		for _, b := range lb {
			if a == b {
				r.Matches = true
			}
		}
	}
	return r
}
