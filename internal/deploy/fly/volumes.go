package fly

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/poll"
	"net/url"
)

const kindVolume = "fly-volume"

func dataMount(id, path string) mount {
	return mount{Volume: id, Path: path, ExtendThreshold: 80, AddGB: 1, LimitGB: 10}
}

func (d *Driver) ensureVolume(ctx context.Context, c *Client, pl plan, p deploy.Placement, part, need string, logf deploy.Logf) (volume, error) {
	name := "at_" + deploy.HostKey(p.ID) + "_" + part
	if need != "" {
		name = "at_" + deploy.HostKey(p.ID) + "_n_" + need
	}
	owned, err := d.Owned(ctx)
	if err != nil {
		return volume{}, err
	}
	path := "/v1/apps/" + url.PathEscape(pl.App) + "/volumes"
	ms, err := c.machines(ctx, pl.App)
	if err != nil {
		return volume{}, err
	}
	group, role := part, "part"
	if need != "" {
		group, role = need, "need"
	}
	for _, m := range ms {
		if !d.ours(m, p) || m.Config.Metadata[metaRole] != role || m.Config.Metadata[metaGroup] != group {
			continue
		}
		cfg, err := m.Config.requestConfig()
		if err != nil {
			return volume{}, err
		}
		for _, mt := range cfg.Mounts {
			for _, o := range owned {
				if o.Kind == kindVolume && o.ID == mt.Volume {
					var v volume
					err := c.do(ctx, "GET", path+"/"+url.PathEscape(o.ID), nil, &v)
					return v, err
				}
			}
		}
	}
	for _, o := range owned {
		if o.Kind == kindVolume && o.Name == name {
			var v volume
			if err := c.do(ctx, "GET", path+"/"+url.PathEscape(o.ID), nil, &v); err != nil {
				return v, err
			}
			return v, nil
		}
	}
	var v volume
	if err := c.do(ctx, "POST", path, map[string]any{"name": name, "region": pl.Region, "size_gb": 1}, &v); err != nil {
		return v, err
	}
	if v.ID == "" {
		return v, fmt.Errorf("Fly returned no volume id for %s", name)
	}
	if err := d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: kindVolume, ID: v.ID, Name: name, Data: true, Need: need}); err != nil {
		return v, err
	}
	logf("made volume %s for %s", v.ID, name)
	return v, nil
}

// A volume stays attached even to a stopped Machine. Wait for stop, then
// destroy it before another Machine can mount the data.
func (d *Driver) removeMachine(ctx context.Context, c *Client, app, id string) error {
	path := "/v1/apps/" + url.PathEscape(app) + "/machines/" + url.PathEscape(id)
	if err := c.do(ctx, "POST", path+"/stop", nil, nil); err != nil {
		return err
	}
	if err := d.awaitStopped(ctx, c, app, id); err != nil {
		return err
	}
	return c.do(ctx, "DELETE", path+"?force=true", nil, nil)
}

func (d *Driver) awaitStopped(ctx context.Context, c *Client, app, id string) error {
	deadline := d.options.Now().Add(d.options.machineWait)
	for {
		m, err := d.get(ctx, c, app, id)
		if err != nil {
			return err
		}
		if m.State == "stopped" && knownHost(m.HostStatus) {
			return nil
		}
		if !d.options.Now().Before(deadline) {
			return fmt.Errorf("Machine %s did not stop", id)
		}
		if err := poll.Wait(ctx, d.options.pollInterval); err != nil {
			return err
		}
	}
}

func knownHost(status string) bool { return status != "unknown" && status != "unreachable" }
