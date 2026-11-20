package appcontract

import (
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/imageline"
	"github.com/google/go-containerregistry/pkg/name"
)

// StaticServerLine is the Caddy family for which AppTown writes its Caddyfile
// and installs Alpine packages. A project may pin a build on it
// (staticServer:); without a pin AppTown serves the newest build it reads
// on this line (imagelines.StaticServer) when a build or preview starts.
const StaticServerLine = "caddy:2-alpine"

// ValidateStaticServer accepts a Caddy 2 Alpine image from Docker Hub named
// by tag and digest: a pin, or the image read for an unpinned contract.
// Empty means no image was chosen for a contract about to be served, which
// a caller resolves first (imagelines.StaticServer): it is refused, never
// replaced by a default here.
func ValidateStaticServer(image string) error {
	if image == "" {
		return fmt.Errorf("staticServer: no server image was resolved for the static parts")
	}
	s, err := imageline.Parse(image, imageline.Line)
	if err != nil {
		return fmt.Errorf("staticServer: %w", err)
	}
	if s.Repository.Name() != "index.docker.io/library/caddy" || strings.Split(s.Prefix, ".")[0] != "2" || s.Variant != "-alpine" {
		return fmt.Errorf("staticServer: use a Caddy 2 Alpine image from Docker Hub")
	}
	if _, err := name.NewDigest(image); err != nil {
		return fmt.Errorf("staticServer: name the image's digest: %w", err)
	}
	return nil
}
func (c *Contract) HasStaticParts() bool {
	for _, p := range c.Parts {
		if p.IsStatic() {
			return true
		}
	}
	return false
}
