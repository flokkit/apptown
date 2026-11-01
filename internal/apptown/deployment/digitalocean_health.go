package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
)

// digitalOceanRelease adds the existing owner's maintained-observation protocol
// to the provider driver; no display operation starts a health sample.
type digitalOceanRelease struct{ *digitalocean.Driver }

func (d *digitalOceanRelease) maintainReadiness(ctx context.Context, p deploy.Placement, artifact string, allowed func(context.Context) error) (deploy.Observation, error) {
	return d.Sample(ctx, p, artifact, allowed)
}
