package deployment

// KindInfo is the catalog of kinds, as data: a new provider is one entry
// here plus its driver. The UI reads this; it never hard-codes a provider.
type KindInfo struct {
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
}

var kindCatalog = []KindInfo{
	{ContainerMemoryLimits: true, Kind: KindDockerHost, Title: "Built-in Docker host", Family: "built-in", Vendor: "this AppTown"},
	{ContainerMemoryLimits: true, Kind: KindPortainer, Title: "Portainer environment", Provider: "portainer", Family: "intranet", Vendor: "Portainer", Environment: true},
	{Kind: KindDOApp, Title: "App Platform", Provider: "digitalocean", Family: "cloud", Vendor: "DigitalOcean", Region: "region"},
	{Kind: KindS3Bucket, Title: "Bucket website", Family: "cloud", Vendor: "S3-compatible storage", Region: "region"},
	// AWS (ECS) and Google Cloud (Cloud Run) land here as one line each
	// with their drivers; nothing in the UI changes.
}

func DescribeKind(kind string) KindInfo {
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
	k := DescribeKind(t.Kind)
	if t.Kind == KindDockerHost && t.Origin != OriginBuiltIn {
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
