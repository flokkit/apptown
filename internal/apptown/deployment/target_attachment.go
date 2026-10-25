package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// TargetAttachment describes a target to observe and optionally attach.
type TargetAttachment struct {
	ConnectionID ids.ID `json:"connectionId"`
	EndpointID   int64  `json:"endpointId"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Host         string `json:"host"`
	KeyFrom      string `json:"keyFrom"`
	URLFrom      string `json:"urlFrom"`
	APIKeyFrom   string `json:"apiKeyFrom"`
	Environment  string `json:"environment"`
	Domain       string `json:"domain"`
	TokenFrom    string `json:"tokenFrom"`
	Region       string `json:"region"`
	Registry     string `json:"registry"`
	// A bucket target: the store's API endpoint, the website address
	// template ({bucket}, {region}) and the secrets holding its key pair.
	Endpoint        string `json:"endpoint"`
	WebsiteEndpoint string `json:"websiteEndpoint"`
	AccessKeyFrom   string `json:"accessKeyFrom"`
	SecretKeyFrom   string `json:"secretKeyFrom"`
	// DryRun: ask the target to answer and report, save nothing — the
	// wizard's connection step, before the person commits.
	DryRun bool `json:"dryRun"`
	// Evals keeps an attached Docker host for evals: its domain is the
	// eval domain, and it takes eval copies only.
	Evals bool `json:"evals"`
}

// TargetAttachmentResult reports provider observations and a committed target.
// A dry run reports observations without creating records or receipts.
type TargetAttachmentResult struct {
	Target   *Target
	Detail   string
	Registry string
}

// AttachTarget owns current authority, intrinsic admission, provider observation
// and create-only persistence. Observation success alone is not attachment.
func (s *Service) AttachTarget(ctx context.Context, in TargetAttachment) (TargetAttachmentResult, error) {
	if s.authority == nil {
		return TargetAttachmentResult{}, access.ErrAuthorityUnavailable
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "deployment.attach-target", Consequential: !in.DryRun}})
	if err != nil {
		return TargetAttachmentResult{}, err
	}
	if actor.Principal.Name == "" {
		return TargetAttachmentResult{}, errors.New("target attachment caller required")
	}

	in.Name = strings.TrimSpace(in.Name)
	if !project.ValidSlug(in.Name) {
		return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("the name must be a slug: lowercase letters, digits and dashes"))
	}
	existing, lookupErr := s.Targets.ByName(ctx, in.Name)
	found, err := database.Lookup(lookupErr)
	if err != nil {
		return TargetAttachmentResult{}, err
	}
	if found && !in.DryRun {
		// A retired target keeps its name with its history; nothing
		// revives it, so the next action is a new name.
		if errors.Is(existing.Status.Available(), ErrTargetRetired) {
			return TargetAttachmentResult{}, fault.Wrap(fault.Conflict, fmt.Errorf("a target named %q was retired; its name stays with its history, so attach this one under a new name", in.Name))
		}
		return TargetAttachmentResult{}, fault.Wrap(fault.Conflict, fmt.Errorf("a target named %q already exists", in.Name))
	}
	domain, err := NormalizeTargetDomain(in.Domain)
	if err != nil {
		return TargetAttachmentResult{}, err
	}
	in.Domain = domain
	if in.Kind == "" {
		in.Kind = KindDockerHost
	}
	if in.Evals && (in.Kind != KindDockerHost || in.Domain == "") {
		return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("only a Docker host with a domain can be kept for evals: its domain is the eval domain"))
	}
	var t *Target
	var detail string
	switch in.Kind {
	case KindDockerHost:
		addr, err := ParseSSHAddress(in.Host)
		if err != nil {
			return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("%v", err))
		}
		in.KeyFrom = strings.TrimSpace(in.KeyFrom)
		if in.KeyFrom == "" {
			return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("keyFrom names the secret holding the SSH private key"))
		}
		ok, words := s.hosts.ProbeHostTarget(ctx, in.Name, addr, in.KeyFrom)
		if !ok {
			return TargetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %s", words))
		}
		detail = words
		needs, err := s.env.needs.EmulatorKinds(ctx)
		if err != nil {
			return TargetAttachmentResult{}, err
		}
		t = &Target{
			Name: in.Name, Kind: KindDockerHost, Provider: "", Origin: OriginAttached,
			Address:      map[string]any{"host": in.Host, "keyFrom": in.KeyFrom, "user": addr.User, "hostname": addr.Host, "port": addr.Port},
			Capabilities: TargetCapabilities{PersistentVolumes: true, Previews: false, OwnLiveness: false, Rollout: []string{"replace"}, Needs: needs},
			Ingress:      map[string]any{},
			NeedsMode:    "emulators", SecretsDelivery: "inject", Status: TargetReady,
		}
	case KindPortainer:
		saved, err := s.AttachPortainerTarget(ctx, PortainerAttachment{Name: in.Name, ConnectionID: in.ConnectionID, EndpointID: in.EndpointID, Environment: strings.TrimSpace(in.Environment), Domain: in.Domain, URLFrom: strings.TrimSpace(in.URLFrom), APIKeyFrom: strings.TrimSpace(in.APIKeyFrom), DryRun: in.DryRun})
		if err != nil {
			return TargetAttachmentResult{}, err
		}
		detail = fmt.Sprintf("Portainer endpoint %v verified", saved.Address["endpoint"])
		if in.DryRun {
			return TargetAttachmentResult{Detail: detail}, nil
		}
		return TargetAttachmentResult{Target: saved, Detail: detail}, nil
	case KindDOApp:
		in.TokenFrom, in.Region = strings.TrimSpace(in.TokenFrom), strings.TrimSpace(in.Region)
		if in.TokenFrom == "" || in.Region == "" {
			return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("a DigitalOcean target needs tokenFrom (the secret holding an API token) and a region"))
		}
		cfg := map[string]any{"tokenFrom": in.TokenFrom, "region": in.Region}
		if in.Registry = strings.TrimSpace(in.Registry); in.Registry != "" {
			cfg["registry"] = in.Registry
		}
		health := s.providers.CheckCloudAccount(ctx, cfg)
		if !health.OK {
			return TargetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %s", health.Detail))
		}
		detail = health.Detail
		// The registry is a fact of the account, not a choice: report it, and
		// only when the account has none does the name to create matter.
		reading, err := s.providers.DigitalOceanRegistry(ctx, digitalocean.Config{TokenFrom: in.TokenFrom})
		if err == nil && reading.Unknown != "" && reading.Registry == "" {
			err = errors.New(reading.Unknown)
		}
		if err != nil {
			return TargetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — asking about the container registry: %v", err))
		}
		registry := reading.Registry
		if registry != "" {
			detail += ", " + reading.Words()
		} else if in.Registry != "" {
			detail += ", container registry " + in.Registry + " to be created on the first release"
		} else {
			detail += ", no container registry yet"
		}
		if in.DryRun {
			return TargetAttachmentResult{Detail: detail, Registry: registry}, nil
		}
		// The account binding, target and receipts are one create-only admission.
		saved, err := s.AttachCloudTarget(ctx, in.Name, cfg, Attachment{By: actor.Principal.Name,
			Attached: map[string]any{"kind": KindDOApp, "region": in.Region, "tokenFrom": in.TokenFrom, "answered": detail},
			Answer:   map[string]any{"ok": true, "detail": detail, "registry": reading}})
		if err != nil {
			return TargetAttachmentResult{}, err
		}
		return TargetAttachmentResult{Target: saved, Detail: detail, Registry: registry}, nil
	case KindS3Bucket:
		address := map[string]any{"endpoint": strings.TrimRight(strings.TrimSpace(in.Endpoint), "/"), "region": strings.TrimSpace(in.Region), "websiteEndpoint": strings.TrimSpace(in.WebsiteEndpoint),
			"accessKeyFrom": strings.TrimSpace(in.AccessKeyFrom), "secretKeyFrom": strings.TrimSpace(in.SecretKeyFrom)}
		for _, k := range []string{"endpoint", "region", "websiteEndpoint", "accessKeyFrom", "secretKeyFrom"} {
			if address[k] == "" {
				return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("a bucket target needs endpoint, region, websiteEndpoint (with {bucket}), accessKeyFrom and secretKeyFrom (secret names)"))
			}
		}
		if !strings.Contains(address["websiteEndpoint"].(string), "{bucket}") {
			return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("websiteEndpoint must name the bucket as {bucket}, such as https://{bucket}.{region}-static.digitaloceanspaces.com"))
		}
		probe := &Target{Kind: KindS3Bucket, Address: address}
		ok, words := s.providers.CheckS3Bucket(ctx, S3BucketConfig(probe))
		if !ok {
			return TargetAttachmentResult{}, fault.Wrap(fault.Unprocessable, fmt.Errorf("not attached — %s", words))
		}
		detail = words
		t = &Target{
			Name: in.Name, Kind: KindS3Bucket, Provider: "", Origin: OriginAttached,
			Address:      address,
			Capabilities: TargetCapabilities{PersistentVolumes: false, Previews: false, OwnLiveness: true, Rollout: []string{"replace"}},
			Ingress:      map[string]any{"binding": "provider-issued"},
			NeedsMode:    "managed", SecretsDelivery: "none", Status: TargetReady,
		}
	default:
		return TargetAttachmentResult{}, fault.Wrap(fault.Invalid, fmt.Errorf("kind must be %s, %s, %s or %s", KindDockerHost, KindPortainer, KindDOApp, KindS3Bucket))
	}
	if in.DryRun {
		return TargetAttachmentResult{Detail: detail}, nil
	}
	if in.Domain != "" {
		t.Ingress["domain"] = in.Domain
	}
	if in.Evals {
		t.Ingress[IngressEvals] = true
	}
	payload := map[string]any{"kind": t.Kind, "answered": detail}
	for _, k := range []string{"hostname", "user", "port", "keyFrom", "urlFrom", "apiKeyFrom", "environment", "endpoint", "region", "websiteEndpoint", "accessKeyFrom", "secretKeyFrom"} {
		if v, ok := t.Address[k]; ok {
			payload[k] = v
		}
	}
	saved, err := s.Targets.Attach(ctx, t, Attachment{By: actor.Principal.Name, Attached: payload, Answer: map[string]any{"ok": true, "detail": detail}})
	if err != nil {
		return TargetAttachmentResult{}, err
	}
	return TargetAttachmentResult{Target: saved, Detail: detail}, nil
}
