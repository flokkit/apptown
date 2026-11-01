package digitalocean

import (
	"context"
	"io"

	"github.com/flokkit/apptown/internal/deploy"
)

const (
	CapCloudAccount = "cloud-account"
	registryHost    = "registry.digitalocean.com"
	// Platform is the one platform App Platform runs, in OCI form.
	Platform = "linux/amd64"
)

// docker is what the driver needs of the container runtime: to run a
// docker command and to read an image's digest.
type docker interface {
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
