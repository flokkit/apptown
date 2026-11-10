package fly

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

// DataMachine is a live reading of a deployment's data consumer. Env is
// used only in memory to reach a need with its current app credentials.
type DataMachine struct {
	ID, Group, Role, State, Image string
	Known                         bool
	Env                           map[string]string
	Volumes                       []dataVolume
}
type dataVolume struct{ ID, Path string }

func (d *Driver) DataMachines(ctx context.Context, p deploy.Placement) ([]DataMachine, error) {
	c, _, err := d.client(ctx)
	if err != nil {
		return nil, err
	}
	app, ok := d.recordedApp(ctx, p)
	if !ok {
		return nil, errors.New("the deployment records no Fly app")
	}
	if err := d.sameApp(ctx, c, app.Name, app.ID); err != nil {
		return nil, err
	}
	ms, err := c.machines(ctx, app.Name)
	if err != nil {
		return nil, err
	}
	var out []DataMachine
	for _, m := range ms {
		if !d.ours(m, p) {
			return nil, fmt.Errorf("Machine %s is not this deployment's", m.ID)
		}
		cfg, err := m.Config.requestConfig()
		if err != nil {
			return nil, err
		}
		v := DataMachine{ID: m.ID, Group: cfg.Metadata[metaGroup], Role: cfg.Metadata[metaRole], State: m.State, Image: cfg.Metadata[metaSourceImage], Env: cfg.Env, Known: knownHost(m.HostStatus)}
		for _, mt := range cfg.Mounts {
			v.Volumes = append(v.Volumes, dataVolume{mt.Volume, mt.Path})
		}
		out = append(out, v)
	}
	return out, nil
}

// StopWriters leaves needs serving, for their declared export/restore. Its
// restart finds each part by group, since a volume copy replaces a Machine.
// The count names the writers stopped; already stopped parts stay stopped.
func (d *Driver) StopWriters(ctx context.Context, p deploy.Placement, before func(string) error, started func(string)) (func(context.Context) []string, int, error) {
	c, _, err := d.client(ctx)
	if err != nil {
		return nil, 0, err
	}
	app, _ := d.recordedApp(ctx, p)
	ms, err := d.DataMachines(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	var groups []string
	stoppedIDs := map[string]string{}
	resume := func(ctx context.Context) []string {
		var failed []string
		now, err := d.DataMachines(ctx, p)
		if err != nil {
			return []string{err.Error()}
		}
		for _, group := range groups {
			found := false
			for _, m := range now {
				if m.Role == "part" && m.Group == group {
					found = true
					path := "/v1/apps/" + url.PathEscape(app.Name) + "/machines/" + url.PathEscape(m.ID) + "/start"
					if err := c.do(ctx, "POST", path, nil, nil); err != nil {
						failed = append(failed, err.Error())
					} else if started != nil {
						started(stoppedIDs[group])
					} else if err := d.awaitReady(ctx, c, app.Name, group, m.ID, func(string, ...any) {}); err != nil {
						failed = append(failed, err.Error())
					}
				}
			}
			if !found {
				failed = append(failed, "part "+group+" is missing")
			}
		}
		return failed
	}
	for _, m := range ms {
		if m.Role != "part" || (m.State == "stopped" && m.Known) {
			continue
		}
		if before != nil {
			if err := before(m.ID); err != nil {
				return resume, len(groups), err
			}
		}
		stoppedIDs[m.Group] = m.ID
		path := "/v1/apps/" + url.PathEscape(app.Name) + "/machines/" + url.PathEscape(m.ID) + "/stop"
		if err := c.do(ctx, "POST", path, nil, nil); err != nil {
			return resume, len(groups), err
		}
		groups = append(groups, m.Group)
		if err := d.awaitStopped(ctx, c, app.Name, m.ID); err != nil {
			return resume, len(groups), err
		}
	}
	return resume, len(groups), nil
}

// DataJob runs a catalogue copy command in a one-off Machine. URLs are
// object-scoped credentials, never logged or checkpointed. Success requires
// both the exporter and upload to complete, then the observed Machine exit.
func (d *Driver) DataJob(ctx context.Context, p deploy.Placement, image, command string, env map[string]string, volumeID, objectURL string, upload bool) error {
	c, conf, err := d.client(ctx)
	if err != nil {
		return err
	}
	app, ok := d.recordedApp(ctx, p)
	if !ok {
		return errors.New("no recorded Fly app")
	}
	image, err = d.copyCatalogueImage(ctx, c, app.Name, image, "copy", func(string, ...any) {})
	if err != nil {
		return err
	}
	script := "set -eu\napk add --no-cache curl >/dev/null\n"
	// Materialize the export before uploading so a failed exporter cannot
	// leave a successful object that is mistaken for a successful copy.
	if upload {
		script += "( " + command + " ) > /tmp/apptown-copy\ncurl --fail --silent --show-error --upload-file /tmp/apptown-copy \"$APPTOWN_COPY_URL\"\n"
	} else {
		script += "curl --fail --silent --show-error --output /tmp/apptown-copy \"$APPTOWN_COPY_URL\"\nprintf '%s  /tmp/apptown-copy\\n' \"$APPTOWN_COPY_SHA256\" | sha256sum -c - >/dev/null\n( " + command + " ) < /tmp/apptown-copy\n"
	}
	cfg := machineConfig{Image: image, Guest: guest{CPUKind: "shared", CPUs: 1, MemoryMB: 512}, Env: map[string]string{}, Init: &machineInit{Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{script}}, Restart: restart{Policy: "no"}, Metadata: d.metadata(map[string]string{metaRole: "copy"}, p, p.Generation)}
	for k, v := range env {
		cfg.Env[k] = v
	}
	cfg.Env["APPTOWN_COPY_URL"] = objectURL
	if volumeID != "" {
		cfg.Mounts = []mount{dataMount(volumeID, "/data")}
	}
	var made machine
	if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app.Name)+"/machines", createMachine{Name: fmt.Sprintf("copy-%d", d.options.Now().UnixNano()), Region: conf.Region, Config: cfg}, &made); err != nil {
		return err
	}
	if made.ID == "" {
		return errors.New("Fly returned no copy Machine id")
	}
	defer d.destroy(context.WithoutCancel(ctx), c, app.Name, made.ID, func(string, ...any) {})
	ex, err := d.awaitExit(ctx, c, app.Name, made.ID)
	if err != nil {
		return err
	}
	if ex.Code != 0 || ex.OOM || ex.Signal != 0 {
		return fmt.Errorf("copy Machine %s exited %d (signal %d, out of memory: %t)", made.ID, ex.Code, ex.Signal, ex.OOM)
	}
	return nil
}

// CopyVolume releases the single attachment, runs the copy, then recreates
// the previous config even when upload fails. A previously stopped part
// stays stopped (final backup and restore keep all writers stopped).
func (d *Driver) CopyVolume(ctx context.Context, p deploy.Placement, id, image, objectURL string) (err error) {
	c, _, err := d.client(ctx)
	if err != nil {
		return err
	}
	app, _ := d.recordedApp(ctx, p)
	ms, err := c.machines(ctx, app.Name)
	if err != nil {
		return err
	}
	for _, m := range ms {
		cfg, e := m.Config.requestConfig()
		if e != nil {
			return e
		}
		for _, mt := range cfg.Mounts {
			if mt.Volume != id {
				continue
			}
			if !d.ours(m, p) || cfg.Metadata[metaRole] != "part" {
				return errors.New("the volume is attached to a Machine that is not an application part")
			}
			if e := d.removeMachine(ctx, c, app.Name, m.ID); e != nil {
				return e
			}
			defer func() {
				body := createMachine{Name: m.Name, Region: m.Region, Config: cfg, SkipLaunch: m.State == "stopped"}
				var made machine
				e := c.do(context.WithoutCancel(ctx), "POST", "/v1/apps/"+url.PathEscape(app.Name)+"/machines", body, &made)
				if e == nil && !body.SkipLaunch {
					e = d.awaitReady(context.WithoutCancel(ctx), c, app.Name, cfg.Metadata[metaGroup], made.ID, func(string, ...any) {})
				}
				err = errors.Join(err, e)
			}()
		}
	}
	return d.DataJob(ctx, p, image, "tar cz -C /data .", nil, id, objectURL, true)
}

// RestoreVolume fills a new volume before changing the part's mount. The
// old volume stays recorded until the new part passes readiness.
func (d *Driver) RestoreVolume(ctx context.Context, p deploy.Placement, id, image, objectURL, expectedSHA256 string) (err error) {
	c, conf, err := d.client(ctx)
	if err != nil {
		return err
	}
	app, _ := d.recordedApp(ctx, p)
	ms, err := c.machines(ctx, app.Name)
	if err != nil {
		return err
	}
	for _, m := range ms {
		cfg, err := m.Config.requestConfig()
		if err != nil {
			return err
		}
		for i, mt := range cfg.Mounts {
			if mt.Volume != id {
				continue
			}
			if !d.ours(m, p) || cfg.Metadata[metaRole] != "part" {
				return errors.New("restore volume is not mounted by an application part")
			}
			if m.State != "stopped" {
				return errors.New("stop every application writer before restoring data")
			}
			var v volume
			name := fmt.Sprintf("restore_%d", d.options.Now().UnixNano())
			path := "/v1/apps/" + url.PathEscape(app.Name)
			if err := c.do(ctx, "POST", path+"/volumes", map[string]any{"name": name, "region": conf.Region, "size_gb": 1}, &v); err != nil {
				return err
			}
			if v.ID == "" {
				return errors.New("Fly returned no restore volume id")
			}
			if err := d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: kindVolume, ID: v.ID, Name: name, Data: true}); err != nil {
				return err
			}
			switched := false
			defer func() {
				if !switched {
					cleanup := c.do(context.WithoutCancel(ctx), "DELETE", path+"/volumes/"+url.PathEscape(v.ID), nil, nil)
					if !isStatus(cleanup, 404) {
						err = errors.Join(err, cleanup)
					}
				}
			}()
			if err := d.DataJob(ctx, p, image, "tar xz -C /data", map[string]string{"APPTOWN_COPY_SHA256": expectedSHA256}, v.ID, objectURL, false); err != nil {
				return err
			}
			old := cfg
			old.Mounts = append([]mount(nil), cfg.Mounts...)
			cfg.Mounts[i] = dataMount(v.ID, mt.Path)
			cfg.Metadata = maps.Clone(cfg.Metadata)
			cfg.Metadata[metaPreviousVolume] = id
			if err := d.removeMachine(ctx, c, app.Name, m.ID); err != nil {
				return err
			}
			var made machine
			if err := c.do(ctx, "POST", path+"/machines", createMachine{Name: m.Name, Region: m.Region, Config: cfg, SkipLaunch: true}, &made); err != nil {
				var previous machine
				recovery := c.do(context.WithoutCancel(ctx), "POST", path+"/machines", createMachine{Name: m.Name, Region: m.Region, Config: old, SkipLaunch: true}, &previous)
				return errors.Join(err, recovery)
			}
			switched = true
			return nil
		}
	}
	return fmt.Errorf("no application part mounts volume %s", id)
}

// ShellCommand joins restore and verify on separate reads of the same
// downloaded copy: the verify must consume the full archive independently.
func ShellCommand(restore, verify string) string {
	if strings.TrimSpace(verify) == "" {
		return restore
	}
	return "( " + restore + " ) < /tmp/apptown-copy && ( " + verify + " ) < /tmp/apptown-copy"
}

const metaPreviousVolume = "apptown_previous_volume"

// VolumeIDs is the provider's current volume list, so a historical owned
// entry whose volume was retired is not mistaken for held data.
func (d *Driver) VolumeIDs(ctx context.Context, p deploy.Placement) (map[string]bool, error) {
	c, _, err := d.client(ctx)
	if err != nil {
		return nil, err
	}
	app, ok := d.recordedApp(ctx, p)
	if !ok {
		return nil, errors.New("no recorded Fly app")
	}
	if err := d.sameApp(ctx, c, app.Name, app.ID); err != nil {
		return nil, err
	}
	var vs []volume
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app.Name)+"/volumes", nil, &vs); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, v := range vs {
		if !v.destroyed() {
			ids[v.ID] = true
		}
	}
	return ids, nil
}

func (d *Driver) retirePreviousVolume(ctx context.Context, c *Client, app string, m machine) error {
	id := m.Config.Metadata[metaPreviousVolume]
	if id == "" {
		return nil
	}
	owned, err := d.Owned(ctx)
	if err != nil {
		return err
	}
	for _, o := range owned {
		if o.Kind == kindVolume && o.ID == id && o.Data {
			err := c.do(ctx, "DELETE", "/v1/apps/"+url.PathEscape(app)+"/volumes/"+url.PathEscape(id), nil, nil)
			if isStatus(err, 404) {
				return nil
			}
			return err
		}
	}
	return fmt.Errorf("previous restore volume %s is not on the owned list; it was kept", id)
}
