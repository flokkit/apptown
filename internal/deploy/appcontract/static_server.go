package appcontract

import (
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/imageline"
	"github.com/google/go-containerregistry/pkg/name"
)

// StaticServerLine is the Caddy family for which AppTown writes its Caddyfile
// and installs Alpine packages. Each project pins the actual build.
const StaticServerLine = "caddy:2-alpine"

func ValidateStaticServer(image string) error {
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

// StaticServerImage refuses execution without a project pin. Parsing an older
// declaration remains possible for readers that do not serve the app.
func StaticServerImage(image string) (string, error) {
	if image == "" {
		return "", fmt.Errorf("This app's static parts name no server image. Add staticServer: to .apptown/app.yaml in a change, using the tag and digest from apptown image newest caddy:2-alpine --scope line.")
	}
	if err := ValidateStaticServer(image); err != nil {
		return "", err
	}
	return image, nil
}
