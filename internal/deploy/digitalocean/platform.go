package digitalocean

import (
	"context"
	"io"

	"flokkit.io/forge/internal/deploy"
)

const (
	CapCloudAccount = "cloud-account"
	RegistryHost    = "registry.digitalocean.com"
	// Platform is the one platform App Platform runs, in OCI form.
	Platform = "linux/amd64"
)

// Secrets is what the renderer asks of a boot's secret delivery: the
// package's word for the app's secrets.
type Secrets = deploy.ConsumerSecrets

// Docker is what the driver needs of the container runtime: to run a
// docker command and to read an image's digest.
type Docker interface {
	Command(ctx context.Context, args []string, stdin io.Reader) ([]byte, error)
	ImageDigest(ctx context.Context, ref string) string
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func configHash(configJSON string) string { return deploy.ConfigHash(configJSON) }
