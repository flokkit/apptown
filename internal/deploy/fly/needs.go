package fly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"reflect"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/poll"
)

const metaSourceImage = "apptown_source_image"

func (d *Driver) standingNeeds(ctx context.Context, c *Client, conf Config, app flyApp, p deploy.Placement, contract *appcontract.Contract, logf deploy.Logf) (map[string]string, []string, error) {
	if len(contract.Needs) == 0 {
		return nil, nil, nil
	}
	if d.Needs == nil {
		return nil, nil, terminal(errors.New("no catalogue runtime for Fly needs"))
	}
	services, _, err := d.Needs(ctx, contract, nil)
	if err != nil {
		return nil, nil, err
	}
	ms, err := c.machines(ctx, app.Name)
	if err != nil {
		return nil, nil, err
	}
	// Refuse incompatible storage engines before copying images, updating any
	// need, or making a volume. A release failure must leave the standing data usable.
	for _, s := range services {
		for _, m := range ms {
			if d.ours(m, p) && m.Config.Metadata[metaRole] == "need" && m.Config.Metadata[metaGroup] == s.Name && s.KeepsImage && m.Config.Metadata[metaSourceImage] != s.Image {
				return nil, nil, terminal(fmt.Errorf("%s keeps its image %s; AppTown will not replace it with %s on its data volume", s.Name, m.Config.Metadata[metaSourceImage], s.Image))
			}
		}
	}
	var credentials []string
	for _, s := range services {
		credentials = append(credentials, s.Credentials...)
	}
	hosts := map[string]string{}
	for _, s := range services {
		var old machine
		for _, m := range ms {
			if d.ours(m, p) && m.Config.Metadata[metaRole] == "need" && m.Config.Metadata[metaGroup] == s.Name {
				old = m
			}
		}
		var oldCfg machineConfig
		if old.ID != "" {
			oldCfg, err = old.Config.requestConfig()
			if err != nil {
				return nil, nil, err
			}
		}
		img := oldCfg.Image
		if img == "" {
			img, err = d.copyCatalogueImage(ctx, c, app.Name, s.Image, "need-"+s.Name, logf)
			if err != nil {
				return nil, nil, err
			}
		}
		g, err := fit(s.Resources)
		if err != nil {
			return nil, nil, terminal(err)
		}
		cfg := machineConfig{Image: img, Guest: g, Env: s.Env, Restart: restart{Policy: "always"}, Metadata: d.metadata(map[string]string{metaRole: "need", metaGroup: s.Name, metaSourceImage: s.Image}, p, 0)}
		if len(s.Command) > 0 || len(s.Entrypoint) > 0 {
			cfg.Init = &machineInit{Cmd: s.Command, Entrypoint: s.Entrypoint}
		}
		if old.ID != "" {
			cfg.Mounts = oldCfg.Mounts
		} else if s.Data != "" {
			v, err := d.ensureVolume(ctx, c, plan{App: app.Name, Region: conf.Region}, p, "", s.Name, logf)
			if err != nil {
				return nil, nil, err
			}
			cfg.Mounts = []mount{dataMount(v.ID, s.Data)}
		}
		if s.Name == "postgres" && s.Data != "" {
			// A Fly volume is ext4 with lost+found at its root, and initdb
			// refuses a non-empty directory. Postgres keeps its cluster in a
			// folder of the mount here; a Docker volume has no such entry, so
			// the stock kind and Docker-host clusters stay as they are.
			cfg.Env = maps.Clone(s.Env)
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			cfg.Env["PGDATA"] = path.Join(s.Data, "pgdata")
		}
		machines := "/v1/apps/" + url.PathEscape(app.Name) + "/machines"
		body := createMachine{Name: "need-" + s.Name, Region: conf.Region, Config: cfg}
		id := old.ID
		if id == "" {
			var m machine
			if err := c.do(ctx, "POST", machines, body, &m); err != nil {
				return nil, nil, err
			}
			id = m.ID
			if id == "" {
				return nil, nil, errors.New("Fly returned no need Machine id")
			}
			if err := d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: "fly-machine", ID: id, Name: body.Name, Need: s.Name}); err != nil {
				return nil, nil, err
			}
		} else if !reflect.DeepEqual(oldCfg, cfg) {
			// Fly updates require the complete config, including the existing mount.
			if err := c.do(ctx, "POST", machines+"/"+url.PathEscape(id), body, nil); err != nil {
				return nil, nil, err
			}
		} else if old.State != "started" {
			if err := c.do(ctx, "POST", machines+"/"+url.PathEscape(id)+"/start", nil, nil); err != nil {
				return nil, nil, err
			}
		}
		if err := d.awaitNeed(ctx, c, app.Name, id, s.Health); err != nil {
			return nil, nil, err
		}
		hosts[s.Name] = id + ".vm." + app.Name + ".internal"
		logf("need %s runs on a Fly Machine, %s, image %s", s.Name, g.words(), img)
	}
	_, env, err := d.Needs(ctx, contract, hosts)
	return env, credentials, err
}

// Copy resolves a catalogue tag once, selects this target's platform, and
// records the resulting digest in the Machine config carried by the release.
func (d *Driver) copyCatalogueImage(ctx context.Context, c *Client, app, ref, tag string, logf deploy.Logf) (string, error) {
	logf("copying catalogue image %s into Fly", ref)
	digest, err := d.options.CopyImage(ctx, oci.Source{Reference: ref}, oci.Repository{Name: registryHost + "/" + app, Credential: oci.Credential{Username: "x", Password: c.Token}}, tag)
	if err != nil {
		return "", fmt.Errorf("copying catalogue image %s: %s", ref, redact(err.Error(), c.Token))
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", errors.New("Fly's registry returned no image digest")
	}
	return registryHost + "/" + app + "@" + digest, nil
}

func (d *Driver) awaitNeed(ctx context.Context, c *Client, app, id, health string) error {
	if health == "" {
		return terminal(errors.New("the need declares no readiness command"))
	}
	deadline := d.options.Now().Add(d.options.machineWait)
	for {
		m, err := d.get(ctx, c, app, id)
		if err != nil {
			return err
		}
		if m.State == "started" {
			var answer struct {
				Code   *int            `json:"exit_code"`
				Signal json.RawMessage `json:"signal"`
			}
			err = c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/exec", map[string]any{"command": []string{"/bin/sh", "-c", health}}, &answer)
			signal := string(answer.Signal)
			if err == nil && answer.Code != nil && *answer.Code == 0 && (signal == "" || signal == "null" || signal == "0" || signal == `""`) {
				return nil
			}
		}
		if !d.options.Now().Before(deadline) {
			return fmt.Errorf("need Machine %s did not pass its readiness command", id)
		}
		if err := poll.Wait(ctx, d.options.pollInterval); err != nil {
			return err
		}
	}
}
