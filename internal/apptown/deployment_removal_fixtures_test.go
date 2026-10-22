package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy"
)

type removalHTTPDriver struct {
	deploy.Driver
	calls int
	err   error
}

func (d *removalHTTPDriver) Teardown(context.Context, deploy.Placement, int64, deploy.TakeDown) (deploy.TakenDown, error) {
	d.calls++
	return deploy.TakenDown{}, d.err
}
