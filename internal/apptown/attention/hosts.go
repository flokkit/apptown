package attention

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Hosts folds uncertainty only for callers who can see the instance's host item.
// Project-only readers retain their deployment conditions and coverage.
type Hosts struct {
	Authority access.ReadAuthorizer
	Observer  hostReachabilityReader
}

// hostReadCache belongs to one sequential Attention build. Every source uses
// the same authorized host observations; no result or grant survives the read.
type hostReadCacheKey struct{}
type hostReadResult struct {
	host deployment.HostReachability
	err  error
}
type hostReadCache map[ids.ID]hostReadResult

func (s Hosts) HostReachability(ctx context.Context, id ids.ID) (deployment.HostReachability, error) {
	cache, _ := ctx.Value(hostReadCacheKey{}).(hostReadCache)
	if result, ok := cache[id]; ok {
		return result.host, result.err
	}
	host, err := s.read(ctx, id)
	if cache != nil {
		cache[id] = hostReadResult{host: host, err: err}
	}
	return host, err
}

func (s Hosts) read(ctx context.Context, id ids.ID) (deployment.HostReachability, error) {
	_, err := s.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "attention.hosts"}})
	if fault.KindOf(err) == fault.Forbidden {
		return deployment.HostReachability{}, nil
	}
	if err != nil {
		return deployment.HostReachability{}, err
	}
	return s.Observer.HostReachability(ctx, id)
}
