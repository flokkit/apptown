package fly

import (
	"context"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/oci"
)

func (d *Driver) ImageStore(_ context.Context, p deploy.Placement) (string, error) {
	app, _ := p.Handle["appName"].(string)
	if app == "" {
		app = appName(d.Installation, deploy.HostKey(p.ID))
	}
	return d.options.registryHost + "/" + app, nil
}
func (d *Driver) TrimImages(ctx context.Context, p deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	c, _, err := d.Client(ctx)
	if err != nil {
		return nil, nil, err
	}
	repo, _ := d.ImageStore(ctx, p)
	deleted, remaining, err := oci.Trim(ctx, oci.Repository{Name: repo, Credential: oci.Credential{Username: "x", Password: c.Token}}, expired, keep)
	left := []deploy.Left{}
	for _, image := range remaining {
		left = append(left, deploy.Left{What: image.Reference, Why: redact(image.Why, c.Token)})
	}
	return deleted, left, err
}

func (d *Driver) HasImages(ctx context.Context, p deploy.Placement, refs []string) (bool, error) {
	c, _, err := d.Client(ctx)
	if err != nil {
		return false, err
	}
	repo, err := d.ImageStore(ctx, p)
	if err != nil {
		return false, err
	}
	return oci.Present(ctx, oci.Repository{Name: repo, Credential: oci.Credential{Username: "x", Password: c.Token}}, refs)
}
