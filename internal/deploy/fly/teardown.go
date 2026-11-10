package fly

import (
	"context"
	"fmt"
	"net/url"

	"github.com/flokkit/apptown/internal/deploy"
)

// Teardown deletes the deployment's Fly app, which takes its Machines,
// addresses, certificates, secrets and registry images with it. It deletes
// only the app its records name (the placement's handle, else the owned
// list) and only after checking, from Fly's answer now, that the app under
// that name still has the recorded id and that every Machine in it carries
// this deployment's mark: a Machine that does not is somebody else's, and
// the take-down stops, naming it. A deployment on this target holds no data
// AppTown keeps, so nothing is copied first.
func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, generation int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	var out deploy.TakenDown
	if p.Generation != generation {
		return out, fmt.Errorf("deployment is at generation %d, not %d — refusing to tear down what was not recorded", p.Generation, generation)
	}
	app, ok := d.recordedApp(ctx, p)
	if !ok {
		for _, o := range td.Owned {
			if o.Kind == kindApp {
				app, ok = flyApp{ID: o.ID, Name: o.Name}, true
			}
		}
	}
	if !ok {
		return out, nil
	}
	c, _, err := d.client(ctx)
	if err != nil {
		return out, err
	}
	var got flyApp
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app.Name), nil, &got); err != nil {
		if isStatus(err, 404) {
			out.Deleted = append(out.Deleted, "Fly app "+app.Name+" (already gone)")
			return out, nil
		}
		return out, err
	}
	if got.ID != app.ID {
		out.Deleted = append(out.Deleted, "Fly app "+app.Name+" (already gone; the name is another app's now, which is left alone)")
		return out, nil
	}
	ms, err := c.machines(ctx, app.Name)
	if err != nil {
		return out, fmt.Errorf("listing the app's Machines before deleting it: %w", err)
	}
	for _, m := range ms {
		if m.State == "destroyed" {
			continue
		}
		if !d.ours(m, p) {
			return out, fmt.Errorf("the Fly app %s holds Machine %s (%s), which does not carry this deployment's mark; AppTown deletes nothing it did not make, so the app stays. Remove that Machine, or move it to another app, then take the deployment down again", app.Name, m.ID, m.Name)
		}
	}
	if err := c.do(ctx, "DELETE", "/v1/apps/"+url.PathEscape(app.Name)+"?force=true", nil, nil); err != nil && !isStatus(err, 404) {
		return out, err
	}
	out.Deleted = append(out.Deleted, "Fly app "+app.Name+" ("+app.ID+") with its Machines, addresses, secrets and images")
	return out, nil
}
