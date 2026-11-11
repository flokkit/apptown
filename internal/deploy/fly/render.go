package fly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/trust"
)

// artifactKind is the Fly driver's artifact: the app, the Machines it
// places for each part and init step, and the secrets they share.
const artifactKind = "fly-machines"

// plan is the artifact's body. Secrets hold values only in memory (the
// body is never stored); the hash is over the body with each value blanked.
type plan struct {
	NeedImages map[string]string `json:"needImages,omitempty"`
	App        string            `json:"app"`
	AppID      string            `json:"appId"`
	Org        string            `json:"org"`
	Region     string            `json:"region"`
	Parts      []placed          `json:"parts"`
	Init       []placed          `json:"init,omitempty"`
	Secrets    map[string]string `json:"secrets,omitempty"`
	Public     string            `json:"public"`
	Ready      string            `json:"readinessPath,omitempty"`
	Release    string            `json:"release,omitempty"`
	RunsOn     map[string]string `json:"runsOn,omitempty"`
	Released   string            `json:"-"`
}

// placed is one Machine's config, by the part or init step it runs.
type placed struct {
	Name   string        `json:"name"`
	Config machineConfig `json:"config"`
}

func encodePlan(pl plan) (deploy.Artifact, error) {
	body, err := json.Marshal(pl)
	if err != nil {
		return deploy.Artifact{}, err
	}
	blank := pl
	blank.Secrets = map[string]string{}
	for k := range pl.Secrets {
		blank.Secrets[k] = "<secret>"
	}
	redacted, err := json.Marshal(blank)
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: artifactKind, Body: body, Hash: deploy.ConfigHash(string(redacted))}, nil
}

func decodePlan(art deploy.Artifact) (plan, error) {
	var pl plan
	if art.Kind != artifactKind {
		return pl, fmt.Errorf("the Fly driver places %s artifacts, not %s", artifactKind, art.Kind)
	}
	if err := json.Unmarshal(art.Body, &pl); err != nil {
		return pl, err
	}
	if pl.App == "" || pl.AppID == "" || len(pl.Parts) == 0 {
		return pl, errors.New("the Fly artifact names no app or no part")
	}
	return pl, nil
}

// Preflight refuses, before any need is fulfilled, any app made or any
// image copied, what Fly cannot run as the contract says: a gated
// placement, an image not by digest, a contract without a public part with
// a port, health Fly cannot honour, a part no Machine size covers, and
// consumers given different secrets (Fly gives every Machine of an app the
// same secrets).
func (d *Driver) Preflight(ctx context.Context, p deploy.Placement, src deploy.Source, secrets deploy.ConsumerSecrets, _ deploy.Logf) error {
	if p.Gate != nil {
		return terminal(errors.New("a Fly target cannot place AppTown's gate yet, so it does not run a restored copy"))
	}
	c := src.Contract
	if c == nil {
		return terminal(errors.New("a Fly release needs its contract"))
	}
	pub := c.PublicPart()
	if pub == nil || pub.Port <= 0 {
		return terminal(errors.New("the contract has no public part with a port; Fly needs a front door"))
	}
	if err := RefusesHealth(c); err != nil {
		return terminal(err)
	}
	for _, part := range c.Parts {
		if _, err := fit(part.Resources); err != nil {
			return terminal(fmt.Errorf("part %s: %w", part.Name, err))
		}
		if ref := src.Images[part.Name]; !strings.Contains(ref, "@sha256:") {
			return terminal(fmt.Errorf("part %s's image is not in a registry by digest; Fly runs only images AppTown copies into its registry by digest", part.Name))
		}
	}
	for _, it := range c.Init {
		if ref := src.Init[it.Name]; !strings.Contains(ref, "@sha256:") {
			return terminal(fmt.Errorf("init step %s's image is not in a registry by digest; Fly runs only images AppTown copies into its registry by digest", it.Name))
		}
	}
	if err := refusesDifferingSecrets(c, secrets); err != nil {
		return err
	}
	if len(c.Needs) > 0 {
		if d.Needs == nil {
			return terminal(errors.New("no catalogue runtime for Fly needs"))
		}
		services, connections, err := d.Needs(ctx, c, nil)
		if err != nil {
			return err
		}
		for _, s := range services {
			if _, err := fit(s.Resources); err != nil {
				return terminal(fmt.Errorf("need %s: %w", s.Name, err))
			}
		}
		selected, err := appcontract.PrepareWired(c, connections)
		if err != nil {
			return terminal(err)
		}
		if err := deploy.RefuseNeedSecretCollisions(selected, slices.Sorted(maps.Keys(connections)), secrets); err != nil {
			return terminal(err)
		}
		var credentials []string
		for _, svc := range services {
			credentials = append(credentials, svc.Credentials...)
		}
		given := map[string]string{}
		maps.Copy(given, connections)
		for _, part := range selected.Parts {
			if err := protectNeedEnv(maps.Clone(part.Env), given, connections, credentials); err != nil {
				return terminal(err)
			}
		}
		for _, it := range selected.Init {
			if err := protectNeedEnv(maps.Clone(it.Env), given, connections, credentials); err != nil {
				return terminal(err)
			}
		}

	}
	return nil
}

// refusesDifferingSecrets says why consumers' secrets cannot be delivered
// on Fly, or nil: Fly gives every Machine of an app every one of the app's
// secrets, so parts and init steps given different secrets would each see
// the others'.
func refusesDifferingSecrets(c *appcontract.Contract, secrets deploy.ConsumerSecrets) error {
	if secrets == nil || c == nil {
		return nil
	}
	consumers := make([]string, 0, len(c.Parts)+len(c.Init))
	for _, p := range c.Parts {
		consumers = append(consumers, p.Name)
	}
	for _, it := range c.Init {
		consumers = append(consumers, "init-"+it.Name)
	}
	if len(consumers) < 2 {
		return nil
	}
	first := secrets.EnvFor(consumers[0])
	for _, other := range consumers[1:] {
		if !maps.Equal(first, secrets.EnvFor(other)) {
			return terminal(fmt.Errorf("Fly gives every Machine of an app the same secrets, and this app gives %s and %s different ones; give its parts and init steps the same secrets, or release it to a Docker host", consumers[0], other))
		}
	}
	return nil
}

// Render makes (or finds, by its idempotency key) the deployment's Fly app,
// copies each release image into the app's repository in Fly's registry by
// digest, and writes the Machines' configs. The app's creation is recorded
// on the deployment's owned list the moment Fly answers it.
func (d *Driver) Render(ctx context.Context, p deploy.Placement, src deploy.Source, bindings deploy.NeedBindings, secrets deploy.ConsumerSecrets, logf deploy.Logf) (deploy.Artifact, error) {
	if err := d.Preflight(ctx, p, src, secrets, logf); err != nil {
		return deploy.Artifact{}, err
	}
	if len(bindings) > 0 {
		return deploy.Artifact{}, terminal(errors.New("Fly runs needs as containers and does not consume managed need bindings"))
	}
	c := src.Contract
	client, cfg, err := d.client(ctx)
	if err != nil {
		return deploy.Artifact{}, err
	}
	app, err := d.ensureApp(ctx, client, cfg, p, logf)
	if err != nil {
		return deploy.Artifact{}, err
	}
	connections, needCredentials, err := d.standingNeeds(ctx, client, cfg, app, p, c, logf)
	if err != nil {
		return deploy.Artifact{}, err
	}
	selected, err := appcontract.PrepareWired(c, connections)
	if err != nil {
		return deploy.Artifact{}, terminal(err)
	}
	needNames := slices.Sorted(maps.Keys(connections))
	if err := deploy.RefuseNeedSecretCollisions(selected, needNames, secrets); err != nil {
		return deploy.Artifact{}, terminal(err)
	}
	url := appURL(app.Name)
	pl := plan{App: app.Name, AppID: app.ID, Org: cfg.Org, Region: cfg.Region, Public: c.PublicPart().Name, Release: c.PublicRelease(src.Revision), RunsOn: map[string]string{}}
	if len(c.Needs) > 0 {
		ms, err := client.machines(ctx, app.Name)
		if err != nil {
			return deploy.Artifact{}, err
		}
		pl.NeedImages = map[string]string{}
		for _, m := range ms {
			if d.ours(m, p) && m.Config.Metadata[metaRole] == "need" {
				cfg, err := m.Config.requestConfig()
				if err != nil {
					return deploy.Artifact{}, err
				}
				pl.NeedImages[m.Config.Metadata[metaGroup]] = cfg.Image
			}
		}
	}
	copied := map[string]string{}
	imageFor := func(name, ref string) (string, error) {
		if got, ok := copied[ref]; ok {
			return got, nil
		}
		got, err := d.copyImage(ctx, client, app.Name, ref, strs.ShortSHA(src.Revision)+"-"+name, logf)
		if err != nil {
			return "", err
		}
		copied[ref] = got
		return got, nil
	}
	if len(c.Parts) > 0 {
		pl.Secrets = map[string]string{}
		consumer := c.Parts[0].Name
		maps.Copy(pl.Secrets, secrets.EnvFor(consumer))
		maps.Copy(pl.Secrets, connections)
	}
	trustScript := trust.Current().Script(false)
	for _, part := range selected.Parts {
		img, err := imageFor(part.Name, src.Images[part.Name])
		if err != nil {
			return deploy.Artifact{}, err
		}
		g, err := fit(part.Resources)
		if err != nil {
			return deploy.Artifact{}, terminal(fmt.Errorf("part %s: %w", part.Name, err))
		}
		pl.RunsOn[part.Name] = g.words()
		env := maps.Clone(part.Env)
		if env == nil {
			env = map[string]string{}
		}
		if err := protectNeedEnv(env, pl.Secrets, connections, needCredentials); err != nil {
			return deploy.Artifact{}, terminal(err)
		}
		// AppTown's wiring wins over the app's own values, as everywhere.
		env["APPTOWN_APP_URL"] = url
		env["APPTOWN_INSTANCE"] = d.Installation
		if part.Public {
			env["APPTOWN_ORIGINS"] = url
		}
		if part.IsStatic() && src.Revision != "" {
			env[appcontract.ReleaseEnvVar] = appcontract.ReleaseIdentity(part.Name, src.Revision)
		}
		if trustScript != "" && !part.IsStatic() {
			env[appcontract.TrustEnvVar] = trustScript
		}
		cfgM := machineConfig{Image: img, Guest: g, Env: env, Restart: restart{Policy: "always"},
			Metadata: map[string]string{metaRole: "part", metaGroup: part.Name},
			DNS:      &dnsConfig{Searches: []string{"process." + app.Name + ".internal"}}}
		if len(part.Command) > 0 {
			cfgM.Init = &machineInit{Cmd: slices.Clone(part.Command)}
		}
		check, err := partCheck(&part)
		if err != nil {
			return deploy.Artifact{}, terminal(err)
		}
		if part.Public {
			svc := service{Protocol: "tcp", InternalPort: part.Port, Autostart: false, Autostop: "off", MinMachinesRunning: 1,
				Ports: []port{{Port: 80, Handlers: []string{"http"}, ForceHTTPS: true}, {Port: 443, Handlers: []string{"tls", "http"}}}}
			if check != nil {
				svc.Checks = []serviceCheck{*check}
				if check.Type == "http" {
					pl.Ready = check.Path
				}
			}
			cfgM.Services = []service{svc}
		} else if check != nil {
			cfgM.Checks = map[string]machineCheck{"readiness": {serviceCheck: *check, Kind: "readiness"}}
		}
		if part.DataDir != "" {
			v, err := d.ensureVolume(ctx, client, pl, p, part.Name, "", logf)
			if err != nil {
				return deploy.Artifact{}, err
			}
			cfgM.Mounts = []mount{dataMount(v.ID, part.DataDir)}
		}
		pl.Parts = append(pl.Parts, placed{Name: part.Name, Config: cfgM})
	}
	for _, it := range selected.Init {
		img, err := imageFor("init-"+it.Name, src.Init[it.Name])
		if err != nil {
			return deploy.Artifact{}, err
		}
		env := maps.Clone(it.Env)
		if env == nil {
			env = map[string]string{}
		}
		if err := protectNeedEnv(env, pl.Secrets, connections, needCredentials); err != nil {
			return deploy.Artifact{}, terminal(err)
		}
		env["APPTOWN_APP_URL"] = url
		env["APPTOWN_INSTANCE"] = d.Installation
		if trustScript != "" {
			env[appcontract.TrustEnvVar] = trustScript
		}
		g, _ := fit(appcontract.Resources{})
		cfgM := machineConfig{Image: img, Guest: g, Env: env, Restart: restart{Policy: "no"},
			Metadata: map[string]string{metaRole: "init"}}
		if len(it.Command) > 0 {
			cfgM.Init = &machineInit{Cmd: slices.Clone(it.Command)}
		}
		pl.Init = append(pl.Init, placed{Name: it.Name, Config: cfgM})
	}
	sort.Slice(pl.Parts, func(i, j int) bool { return pl.Parts[i].Name < pl.Parts[j].Name })
	for name, words := range pl.RunsOn {
		logf("part %s runs on a Fly Machine, %s", name, words)
	}
	return encodePlan(pl)
}

// ensureApp makes the deployment's Fly app, or gets back the one an earlier
// release made, by Fly's idempotency key (the deployment's id): a retry of
// the same create returns the same app. An app of that name Fly made for
// someone else (name taken) is refused, never adopted. The app is recorded
// on the deployment's owned list before anything else touches it.
func (d *Driver) ensureApp(ctx context.Context, c *Client, cfg Config, p deploy.Placement, logf deploy.Logf) (flyApp, error) {
	if d.Installation == "" {
		return flyApp{}, terminal(errors.New("this AppTown has no installation id, which names and marks what it makes on Fly"))
	}
	if d.Owner == nil {
		return flyApp{}, terminal(errors.New("no owned list to record the Fly app on; nothing is made"))
	}
	name := appName(d.Installation, deploy.HostKey(p.ID))
	if recorded, ok := d.recordedApp(ctx, p); ok {
		var got flyApp
		switch err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(recorded.Name), nil, &got); {
		case err == nil && got.ID == recorded.ID:
			return got, nil
		case err == nil:
			return flyApp{}, terminal(fmt.Errorf("the Fly app %s is %s, not the app %s AppTown recorded making; it is not this deployment's", recorded.Name, got.ID, recorded.ID))
		case !isStatus(err, 404):
			return flyApp{}, err
		}
		logf("the Fly app %s AppTown recorded is gone; making it again", recorded.Name)
	}
	var made struct {
		ID string `json:"id"`
	}
	body := map[string]any{"name": name, "org_slug": cfg.Org, "idempotency_key": "apptown-" + p.ID.String()}
	if err := c.do(ctx, "POST", "/v1/apps", body, &made); err != nil {
		var e *apiError
		if errors.As(err, &e) && e.Status == 422 {
			return flyApp{}, terminal(fmt.Errorf("Fly refused the app name %s (%s); another app holds it, and AppTown never adopts an app by its name", name, e.Msg))
		}
		return flyApp{}, fmt.Errorf("making the Fly app %s: %w", name, err)
	}
	if made.ID == "" {
		return flyApp{}, fmt.Errorf("Fly made the app %s without naming its id", name)
	}
	if err := d.Owner.Created(ctx, deploy.Owned{Kind: kindApp, ID: made.ID, Name: name}); err != nil {
		return flyApp{}, fmt.Errorf("recording the Fly app %s AppTown made: %w", name, err)
	}
	logf("made the Fly app %s in the org %s", name, cfg.Org)
	return flyApp{ID: made.ID, Name: name}, nil
}

// kindApp is the owned-list kind of a deployment's Fly app.
const kindApp = "fly-app"

// recordedApp is the Fly app the deployment's records name: the placement's
// handle, else its owned list.
func (d *Driver) recordedApp(ctx context.Context, p deploy.Placement) (flyApp, bool) {
	id, _ := p.Handle["appId"].(string)
	name, _ := p.Handle["appName"].(string)
	if id != "" && name != "" {
		return flyApp{ID: id, Name: name}, true
	}
	if d.Owned == nil {
		return flyApp{}, false
	}
	owned, err := d.Owned(ctx)
	if err != nil {
		return flyApp{}, false
	}
	for _, o := range owned {
		if o.Kind == kindApp {
			return flyApp{ID: o.ID, Name: o.Name}, true
		}
	}
	return flyApp{}, false
}

// copyImage copies a release image by digest into the app's repository in
// Fly's registry, keeping its digest, and answers the reference a Machine
// pulls. Fly's registry takes an org token as the password.
func (d *Driver) copyImage(ctx context.Context, c *Client, app, ref, tag string, logf deploy.Logf) (string, error) {
	_, want, ok := strings.Cut(ref, "@")
	if !ok {
		return "", terminal(fmt.Errorf("%s is not an image by digest", ref))
	}
	from := oci.Source{Reference: ref}
	if d.ReleaseLogin != nil {
		cred, err := d.ReleaseLogin(ref)
		if err != nil {
			return "", err
		}
		from.Credential = cred
	}
	repo := d.options.registryHost + "/" + app
	logf("copying %s into %s:%s", ref, repo, tag)
	got, err := d.options.CopyImage(ctx, from, oci.Repository{Name: repo, Credential: oci.Credential{Username: "x", Password: c.Token}}, tag)
	if err != nil {
		return "", fmt.Errorf("copying %s into %s: %s", ref, repo, redact(err.Error(), c.Token))
	}
	if got != want {
		return "", fmt.Errorf("copying %s into %s: Fly's registry answered %s", ref, repo, got)
	}
	return repo + "@" + want, nil
}

func generationWord(g int64) string { return "generation " + strconv.FormatInt(g, 10) }

func parseGeneration(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "generation "), 10, 64)
	return n, err == nil && n > 0
}

// handleGeneration is the generation a handle records running.
func handleGeneration(h map[string]any) (int64, bool) {
	switch v := h["generation"].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

// Connection outputs and values containing generated credentials use Fly
// secrets. Ordinary per-consumer environment values remain per-consumer.
func protectNeedEnv(env, given, connections map[string]string, credentials []string) error {
	for key, value := range env {
		if _, injected := connections[key]; injected {
			delete(env, key)
			continue
		}
		for _, secret := range credentials {
			if secret != "" && strings.Contains(value, secret) {
				if previous, ok := given[key]; ok && previous != value {
					return fmt.Errorf("Fly gives every Machine the same secret %s, but this release gives its consumers different values for it", key)
				}
				given[key] = value
				delete(env, key)
				break
			}
		}
	}
	return nil
}
