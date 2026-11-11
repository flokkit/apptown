package awsecs

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// ParameterPath exposes a placement's SSM prefix to the external tests.
func ParameterPath(d *Driver, p deploy.Placement) string { return d.path(p) }

// EnsureRoute exposes the listener-rule step of a render to the external tests.
func EnsureRoute(d *Driver, ctx context.Context, a *API, c Config, e Estate, p deploy.Placement, group string, part appcontract.Part) (string, string, error) {
	return d.ensureRoute(ctx, a, c, e, p, group, part)
}
