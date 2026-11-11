package deployment

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/fly"
)

// CopyProtocol distinguishes managed copies reached through a named host from
// complete per-instance inventories and copies run by the provider's own jobs.
type copyProtocol uint8

const (
	copyNone copyProtocol = iota
	copyNamedHost
	CopyDedicatedPostgres
)

// CopyProtocol is the kind's data execution contract; it is not an editable
// target setting. Provider jobs keep their network through final verification.
func (t *Target) CopyProtocol() copyProtocol { return describeKind(t.Kind).copyProtocol }

// KindInfo is the catalog of kinds and their local admission and data contracts.
// Provider mechanisms remain with their drivers; the UI reads the display fields.
type KindInfo struct {
	copyProtocol    copyProtocol
	contractRefusal func(*appcontract.Contract) string
	audienceAddress string
	managedData     func(*Providers, *Target) deploy.ManagedData
	featureArea     string
	// creationDescription is the one-line placement choice, owned by the kind catalog.
	creationDescription string
	// ContainerMemoryLimits permits direct caps/defaults and explicit unlimited overrides.
	ContainerMemoryLimits bool   `json:"containerMemoryLimits"`
	Kind                  string `json:"kind"`
	Title                 string `json:"title"`
	Provider              string `json:"provider"` // connector provider name, "" for built-in
	Family                string `json:"family"`   // built-in | attached | intranet | cloud
	Vendor                string `json:"vendor"`   // DigitalOcean, AWS, Google Cloud — for the icon and the sentence
	Region                string `json:"region"`   // the address key that names where it is, "" if placeless
	// Environment says the target releases into one of its provider's
	// environments, named by the administrator (AddressEnvironment).
	Environment bool `json:"environment"`
	// Guide is the slug of the kind's setup guide under stock/docs/guides,
	// which the product links at /docs/guides/<guide>.
	Guide string `json:"guide"`
	// platform is the one platform a cloud kind runs ("" where the target
	// is asked); publishedOnly says it runs only images in a registry by
	// digest, with no local image to push.
	platform      string
	publishedOnly bool
}

var kindCatalog = []KindInfo{
	{copyProtocol: CopyDedicatedPostgres, contractRefusal: awsecs.RefusesContract, audienceAddress: "load balancer", managedData: func(p *Providers, t *Target) deploy.ManagedData { return p.awsManaged(t) }, creationDescription: "AWS ECS on Fargate · AWS", Kind: KindAWSECS, Guide: "aws-target-setup", featureArea: "aws-ecs", Title: "AWS ECS on Fargate", Provider: "aws", Family: "cloud", Vendor: "AWS", Region: "region", platform: "linux/amd64", publishedOnly: true},
	{ContainerMemoryLimits: true, creationDescription: "Built-in Docker host · this AppTown", Kind: KindDockerHost, Guide: "docker-host-target-setup", featureArea: "deployments", Title: "Built-in Docker host", Family: "built-in", Vendor: "this AppTown"},
	{ContainerMemoryLimits: true, creationDescription: "Portainer environment · Portainer", Kind: KindPortainer, Guide: "portainer-target-setup", featureArea: "portainer", Title: "Portainer environment", Provider: "portainer", Family: "intranet", Vendor: "Portainer", Environment: true},
	{copyProtocol: copyNamedHost, managedData: func(p *Providers, t *Target) deploy.ManagedData {
		return digitalocean.ManagedData{Client: p.cloudClientFor(t.Provider), OwnDatabase: p.digitalOcean.OwnDatabase}
	}, creationDescription: "App Platform · DigitalOcean", Kind: KindDOApp, Guide: "app-platform-target-setup", featureArea: "app-platform", Title: "App Platform", Provider: "digitalocean", Family: "cloud", Vendor: "DigitalOcean", Region: "region", platform: digitalocean.Platform},
	{audienceAddress: "fly.dev", creationDescription: "Fly.io Machines · Fly.io", Kind: kindFlyMachines, Guide: "fly-target-setup", featureArea: "fly-machines", Title: "Fly.io Machines", Provider: "fly", Family: "cloud", Vendor: "Fly.io", Region: "region", platform: fly.Platform, publishedOnly: true},
	{audienceAddress: "website", creationDescription: "Bucket website · static sites only", Kind: KindS3Bucket, Guide: "bucket-website-target-setup", featureArea: "bucket-site", Title: "Bucket website", Family: "cloud", Vendor: "S3-compatible storage", Region: "region"},
	// Other providers land here as one line each
	// with their drivers; nothing in the UI changes.
}

func describeKind(kind string) KindInfo {
	for _, k := range kindCatalog {
		if k.Kind == kind {
			return k
		}
	}
	return KindInfo{Kind: kind, Title: kind, Family: "cloud"}
}

// DescribeTarget is a target's kind as people read it. The Docker host kind
// describes this AppTown's own host; a Docker host attached over SSH is
// described by its origin, placed at its hostname, never as "this AppTown".
func DescribeTarget(t *Target) KindInfo {
	k := describeKind(t.Kind)
	if t.Kind == KindDockerHost && t.Origin != OriginBuiltIn {
		k.creationDescription = "Docker host · over SSH"
		k.Title, k.Family, k.Vendor, k.Region = "Docker host", "attached", "over SSH", "hostname"
	}
	return k
}

// TargetPlace is the address value that names where a target is (a region,
// an attached host's hostname); "" when it is placeless.
func TargetPlace(t *Target) string {
	k := DescribeTarget(t)
	if k.Region == "" {
		return ""
	}
	place, _ := t.Address[k.Region].(string)
	return place
}

func Kinds() []KindInfo { return append([]KindInfo(nil), kindCatalog...) }
