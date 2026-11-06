package digitalocean

import (
	"context"
	"errors"
	"net/url"

	"github.com/flokkit/apptown/internal/deploy"
)

// Runtime uses App Platform's documented restart operation. It restarts the
// existing images, without creating a deployment from source or a backup.
// https://docs.digitalocean.com/reference/api/reference/apps/#operation/apps_restart
func (d *Driver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	if action != "restart" {
		return errors.New("App Platform cannot keep an app stopped")
	}
	id, _ := p.Handle["appId"].(string)
	if id == "" {
		return errors.New("the deployment has no recorded App Platform app")
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	var result struct {
		Deployment doDeployment `json:"deployment"`
	}
	if err := c.Call(ctx, "POST", "/v2/apps/"+url.PathEscape(id)+"/restart", map[string]any{}, &result); err != nil {
		return err
	}
	if result.Deployment.ID == "" {
		return errors.New("App Platform did not identify its restart")
	}
	_, err = d.waitDeployment(ctx, c, id, result.Deployment.ID, false, func(string, ...any) {})
	return err
}

func (d *Driver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	return d.observe(ctx, p)
}
