package attention

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

type instanceAccessKey struct{}

// Only an instance reader can fold a project condition into a visible host item.
func hostReachability(ctx context.Context, reader hostReachabilityReader, id ids.ID) (deployment.HostReachability, error) {
	if allowed, present := ctx.Value(instanceAccessKey{}).(bool); present && !allowed {
		return deployment.HostReachability{}, nil
	}
	return reader.HostReachability(ctx, id)
}
