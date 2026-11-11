package portainer

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func (d *Driver) imageScope(ctx context.Context) (docker.Scope, error) {
	if d.ImageScope == nil {
		return docker.Scope{}, fmt.Errorf("this Portainer endpoint cannot be reached for image retention")
	}
	return d.ImageScope(ctx)
}
func (d *Driver) ImageStore(ctx context.Context, _ deploy.Placement) (string, error) {
	scope, err := d.imageScope(ctx)
	if err != nil {
		return "", err
	}
	return scope.ImageStore(ctx)
}
func (d *Driver) TrimImages(ctx context.Context, _ deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	scope, err := d.imageScope(ctx)
	if err != nil {
		return nil, nil, err
	}
	return scope.TrimImages(ctx, expired, keep, "")
}

func (d *Driver) HasImages(ctx context.Context, _ deploy.Placement, refs []string) (bool, error) {
	scope, err := d.imageScope(ctx)
	if err != nil {
		return false, err
	}
	return scope.HasImages(ctx, refs)
}
