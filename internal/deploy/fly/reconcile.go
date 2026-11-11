package fly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
)

// Reconcile places the artifact as generation g. It never updates a Machine
// in place for application parts: it sets the app's secrets, runs each init step as a one-off
// Machine, makes a new Machine for each part, waits for each to start and
// pass its checks, witnesses the front door, and only then destroys the
// previous generation's Machines. Every Machine's id is checkpointed the
// moment Fly answers its creation. A failure before the old generation is
// destroyed destroys the new one and restores data parts from their checkpointed
// previous configs on the same volumes. Standing needs keep their identities.
func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, g int64, art deploy.Artifact, _ string, logf deploy.Logf, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	pl, err := decodePlan(art)
	if err != nil {
		return deploy.Observation{}, terminal(err)
	}
	c, _, err := d.client(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	if err := d.sameApp(ctx, c, pl.App, pl.AppID); err != nil {
		return deploy.Observation{}, err
	}
	noted := map[string]any{"appId": pl.AppID, "appName": pl.App, "generation": g, "public": pl.Public, "release": pl.Release, "readinessPath": pl.Ready}
	if len(pl.NeedImages) > 0 {
		noted["needImages"] = pl.NeedImages
	}
	if err := checkpoint(noted); err != nil {
		return deploy.Observation{}, fmt.Errorf("persisting the provider response: %w", err)
	}
	if err := deploy.ReportStage(ctx, deploy.Starting); err != nil {
		return deploy.Observation{}, err
	}
	version, err := d.setSecrets(ctx, c, pl.App, pl.Secrets, logf)
	if err != nil {
		return deploy.Observation{}, err
	}
	for _, it := range pl.Init {
		if err := d.runInit(ctx, c, p, pl, it, g, version, logf, checkpoint, noted); err != nil {
			return deploy.Observation{}, err
		}
	}
	if err := d.ensureIPs(ctx, c, pl.App, logf); err != nil {
		return deploy.Observation{}, err
	}
	// The whole set of parts this generation must have is recorded before
	// any is made, so recovery and observation judge the generation against
	// it, never against the Machines that happen to exist.
	noted["parts"] = partList(pl.Parts)
	if err := checkpoint(noted); err != nil {
		return deploy.Observation{}, fmt.Errorf("persisting the provider response: %w", err)
	}
	made := map[string]string{}
	previous := map[string]createMachine{}
	fail := func(err error) (deploy.Observation, error) {
		d.destroyAll(ctx, c, pl.App, made, logf)
		recovery := d.restorePrevious(context.WithoutCancel(ctx), c, p, pl.App, previous, logf)
		return deploy.Observation{}, errors.Join(err, recovery)
	}
	for _, part := range pl.Parts {
		previousVolume := ""
		if len(part.Config.Mounts) > 0 {
			ms, err := c.machines(ctx, pl.App)
			if err != nil {
				return fail(err)
			}
			for _, old := range ms {
				if !d.ours(old, p) || old.Config.Metadata[metaRole] != "part" || old.Config.Metadata[metaGroup] != part.Name {
					continue
				}
				cfg, err := old.Config.requestConfig()
				if err != nil {
					return fail(err)
				}
				previousVolume = cfg.Metadata[metaPreviousVolume]
				body := createMachine{Name: old.Name, Region: old.Region, Config: cfg, SkipLaunch: old.State == "stopped"}
				previous[part.Name] = body
				noted["previous:"+part.Name] = body
				if err := checkpoint(noted); err != nil {
					return fail(fmt.Errorf("recording the previous part before releasing its volume: %w", err))
				}
				if err := d.removeMachine(ctx, c, pl.App, old.ID); err != nil {
					return fail(err)
				}
			}
		}
		cfg := part.Config
		cfg.Metadata = d.metadata(cfg.Metadata, p, g)
		if previousVolume != "" {
			cfg.Metadata[metaPreviousVolume] = previousVolume
		}
		var m machine
		body := createMachine{Name: machineName(part.Name, g), Region: pl.Region, Config: cfg, MinSecretsVersion: version}
		if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(pl.App)+"/machines", body, &m); err != nil {
			return fail(fmt.Errorf("making the Machine for part %s: %w", part.Name, err))
		}
		made[part.Name] = m.ID
		// A checkpoint never changes what a key named: each Machine has a key
		// of its own.
		noted["machine:"+part.Name] = m.ID
		if err := checkpoint(noted); err != nil {
			return fail(fmt.Errorf("persisting the provider response: %w", err))
		}
		logf("made Machine %s for part %s (generation %d)", m.ID, part.Name, g)
	}
	if err := deploy.ReportStage(ctx, deploy.Checking); err != nil {
		return fail(err)
	}
	for _, part := range pl.Parts {
		if err := d.awaitReady(ctx, c, pl.App, part.Name, made[part.Name], logf); err != nil {
			return fail(err)
		}
	}
	witnessed, err := d.witness(ctx, pl, logf)
	if err != nil {
		return fail(err)
	}
	d.retireOthers(ctx, c, p, pl.App, g, logf)
	health := "unknown"
	if witnessed {
		health = "healthy"
	}
	return deploy.Observation{Generation: g, Rollout: "active", Health: health, URL: appURL(pl.App), Running: generationWord(g),
		Handle: map[string]any{"appId": pl.AppID, "appName": pl.App, "generation": g, "parts": noted["parts"], "machines": made}}, nil
}

// partList is a generation's parts, sorted and comma-separated.
func partList(parts []placed) string {
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// expectedParts is the part set a handle or a checkpoint recorded; nil
// when none was recorded.
func expectedParts(h map[string]any) []string {
	s, _ := h["parts"].(string)
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Resume finishes an attempt AppTown was interrupted in: when every part's
// Machine of the generation stands and is ready, the release is witnessed
// and the old generation retired; otherwise the generation's Machines are
// destroyed and the attempt fails, leaving the previous one serving.
func (d *Driver) Resume(ctx context.Context, p deploy.Placement, g int64, noted map[string]any, logf deploy.Logf) (deploy.Observation, error) {
	app, _ := noted["appName"].(string)
	appID, _ := noted["appId"].(string)
	if app == "" {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the attempt was interrupted before Fly named its app: %w", deploy.ErrNotApplied))
	}
	c, _, err := d.client(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	if err := d.sameApp(ctx, c, app, appID); err != nil {
		return deploy.Observation{}, err
	}
	ms, err := c.machines(ctx, app)
	if err != nil {
		return deploy.Observation{}, err
	}
	mine := map[string]string{}
	ready := true
	for _, m := range ms {
		if !d.ours(m, p) || m.Config.Metadata[metaGeneration] != strconv.FormatInt(g, 10) {
			continue
		}
		if m.Config.Metadata[metaRole] == "need" {
			continue
		}
		if m.Config.Metadata[metaRole] == "init" {
			d.destroy(ctx, c, app, m.ID, logf)
			continue
		}
		mine[m.Config.Metadata[metaGroup]] = m.ID
		ready = ready && m.State == "started" && checksPass(m)
	}
	public, _ := noted["public"].(string)
	expected := expectedParts(noted)
	complete := len(expected) > 0 && len(mine) == len(expected)
	for _, part := range expected {
		complete = complete && mine[part] != ""
	}
	if !ready || !complete || mine[public] == "" {
		d.destroyAll(ctx, c, app, mine, logf)
		previous := map[string]createMachine{}
		for key, value := range noted {
			if name, ok := strings.CutPrefix(key, "previous:"); ok {
				raw, err := json.Marshal(value)
				if err != nil {
					return deploy.Observation{}, err
				}
				var body createMachine
				if err := json.Unmarshal(raw, &body); err != nil {
					return deploy.Observation{}, err
				}
				previous[name] = body
			}
		}
		err := d.restorePrevious(ctx, c, p, app, previous, logf)
		return deploy.Observation{Generation: g, Rollout: "failed", Health: "unknown"}, errors.Join(errors.New("the release was interrupted before all of its Machines were ready"), err)
	}
	for _, m := range ms {
		if d.ours(m, p) && m.Config.Metadata[metaRole] == "part" && m.Config.Metadata[metaGeneration] == strconv.FormatInt(g, 10) {
			if err := d.retirePreviousVolume(ctx, c, app, m); err != nil {
				return deploy.Observation{}, err
			}
		}
	}
	release, _ := noted["release"].(string)
	path, _ := noted["readinessPath"].(string)
	witnessed, err := d.witness(ctx, plan{App: app, Release: release, Ready: path}, logf)
	if err != nil {
		d.destroyAll(ctx, c, app, mine, logf)
		return deploy.Observation{}, err
	}
	d.retireOthers(ctx, c, p, app, g, logf)
	health := "unknown"
	if witnessed {
		health = "healthy"
	}
	return deploy.Observation{Generation: g, Rollout: "active", Health: health, URL: appURL(app), Running: generationWord(g),
		Handle: map[string]any{"appId": appID, "appName": app, "generation": g, "parts": noted["parts"], "machines": mine}}, nil
}

// sameApp checks the app Fly has under name is the one AppTown recorded.
func (d *Driver) sameApp(ctx context.Context, c *Client, name, id string) error {
	var got flyApp
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(name), nil, &got); err != nil {
		return fmt.Errorf("reading the Fly app %s: %w", name, err)
	}
	if got.ID != id {
		return terminal(fmt.Errorf("the Fly app %s is %s, not %s as recorded; refusing to place Machines in it", name, got.ID, id))
	}
	return nil
}

func (d *Driver) metadata(base map[string]string, p deploy.Placement, g int64) map[string]string {
	m := maps.Clone(base)
	if m == nil {
		m = map[string]string{}
	}
	m[metaInstance] = d.Installation
	m[metaDeployment] = p.ID.String()
	m[metaGeneration] = strconv.FormatInt(g, 10)
	return m
}

// ours says whether a Machine carries this installation's and this
// deployment's marks.
func (d *Driver) ours(m machine, p deploy.Placement) bool {
	return m.Config.Metadata[metaInstance] == d.Installation && m.Config.Metadata[metaDeployment] == p.ID.String()
}

// setSecrets makes the app's secrets exactly these in one change, unsetting
// any the release no longer gives, and answers the secrets version the
// new Machines must see.
func (d *Driver) setSecrets(ctx context.Context, c *Client, app string, values map[string]string, logf deploy.Logf) (int64, error) {
	var have struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	path := "/v1/apps/" + url.PathEscape(app) + "/secrets"
	if err := c.do(ctx, "GET", path, nil, &have); err != nil {
		return 0, fmt.Errorf("reading the app's secret names: %w", err)
	}
	change := map[string]*string{}
	for k, v := range values {
		v := v
		change[k] = &v
	}
	for _, s := range have.Secrets {
		if _, keep := values[s.Name]; !keep {
			change[s.Name] = nil
		}
	}
	if len(change) == 0 {
		return 0, nil
	}
	var got struct {
		Version int64 `json:"version"`
	}
	if err := c.do(ctx, "POST", path, map[string]any{"values": change}, &got); err != nil {
		return 0, fmt.Errorf("setting the app's secrets: %w", err)
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > 0 {
		logf("gave the app its secrets (%s) as Fly secrets, version %d", strings.Join(names, ", "), got.Version)
	}
	return got.Version, nil
}

// runInit runs one init step as a one-off Machine, reads its exit from
// Fly's events and destroys it. A non-zero exit fails the release.
func (d *Driver) runInit(ctx context.Context, c *Client, p deploy.Placement, pl plan, it placed, g, version int64, logf deploy.Logf, checkpoint deploy.Checkpoint, noted map[string]any) error {
	cfg := it.Config
	cfg.Metadata = d.metadata(cfg.Metadata, p, g)
	var m machine
	body := createMachine{Name: machineName("init-"+it.Name, g), Region: pl.Region, Config: cfg, MinSecretsVersion: version}
	if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(pl.App)+"/machines", body, &m); err != nil {
		return fmt.Errorf("making the Machine for init step %s: %w", it.Name, err)
	}
	noted["init:"+it.Name] = m.ID
	if err := checkpoint(noted); err != nil {
		d.destroy(ctx, c, pl.App, m.ID, logf)
		return fmt.Errorf("persisting the provider response: %w", err)
	}
	defer d.destroy(ctx, c, pl.App, m.ID, logf)
	logf("running init step %s on Machine %s", it.Name, m.ID)
	ex, err := d.awaitExit(ctx, c, pl.App, m.ID)
	if err != nil {
		return fmt.Errorf("init step %s: %w", it.Name, err)
	}
	if ex.failed() {
		oom := ""
		if ex.OOM {
			oom = ", out of memory"
		}
		return terminal(fmt.Errorf("init step %s exited %d%s; its output is in Fly's logs for Machine %s", it.Name, ex.Code, oom, m.ID))
	}
	logf("init step %s exited 0", it.Name)
	return nil
}

// awaitExit waits for a one-off Machine to stop and reads its exit.
func (d *Driver) awaitExit(ctx context.Context, c *Client, app, id string) (exit, error) {
	deadline := d.options.Now().Add(d.options.machineWait)
	for {
		m, err := d.get(ctx, c, app, id)
		if err != nil {
			return exit{}, err
		}
		switch m.State {
		case "stopped", "failed", "destroyed":
			var events []event
			if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"/events?limit=50", nil, &events); err != nil {
				return exit{}, fmt.Errorf("reading how Machine %s exited: %w", id, err)
			}
			if ex, ok := exitOf(events); ok {
				return ex, nil
			}
			if m.State == "failed" {
				return exit{}, terminal(fmt.Errorf("Machine %s failed to launch", id))
			}
			return exit{}, fmt.Errorf("Machine %s stopped and Fly reported no exit for it", id)
		}
		if d.options.Now().After(deadline) {
			return exit{}, fmt.Errorf("Machine %s is still %s after %s", id, m.State, d.options.machineWait)
		}
		if err := poll.Wait(ctx, d.options.pollInterval); err != nil {
			return exit{}, err
		}
	}
}

// exitOf is the newest exit an event reports.
func exitOf(events []event) (exit, bool) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp > events[j].Timestamp })
	for _, e := range events {
		raw, ok := e.Request["exit_event"].(map[string]any)
		if !ok {
			if mon, ok := e.Request["MonitorEvent"].(map[string]any); ok {
				raw, ok = mon["exit_event"].(map[string]any)
				if !ok {
					continue
				}
			} else {
				continue
			}
		}
		code, ok := raw["exit_code"].(float64)
		if !ok {
			continue
		}
		ex := exit{Code: int(code)}
		if signal, ok := raw["signal"].(float64); ok {
			ex.Signal = int(signal)
		}
		ex.OOM, _ = raw["oom_killed"].(bool)
		return ex, true
	}
	return exit{}, false
}

// awaitReady waits for a part's new Machine to be started and its checks
// to pass. A Machine that fails or stops before then fails the release.
func (d *Driver) awaitReady(ctx context.Context, c *Client, app, part, id string, logf deploy.Logf) error {
	deadline := d.options.Now().Add(d.options.machineWait)
	last := ""
	for {
		m, err := d.get(ctx, c, app, id)
		if err != nil {
			return err
		}
		switch {
		case m.State == "started" && checksPass(m):
			logf("part %s's Machine %s is started and passes its checks", part, id)
			return d.retirePreviousVolume(ctx, c, app, m)
		case m.State == "failed" || m.State == "stopped" || m.State == "destroyed":
			return terminal(fmt.Errorf("part %s's Machine %s %s before it was ready; its output is in Fly's logs", part, id, m.State))
		}
		now := describe(m)
		if now != last {
			logf("part %s's Machine %s is %s", part, id, now)
			last = now
		}
		if d.options.Now().After(deadline) {
			return fmt.Errorf("part %s's Machine %s is still %s after %s", part, id, now, d.options.machineWait)
		}
		if err := poll.Wait(ctx, d.options.pollInterval); err != nil {
			return err
		}
	}
}

func checksPass(m machine) bool {
	for _, ch := range m.Checks {
		if ch.Status != "passing" {
			return false
		}
	}
	return true
}

func describe(m machine) string {
	var failing []string
	for _, ch := range m.Checks {
		if ch.Status != "passing" {
			failing = append(failing, ch.Name+" "+ch.Status)
		}
	}
	if len(failing) == 0 {
		return m.State
	}
	return m.State + " (" + strings.Join(failing, ", ") + ")"
}

func (d *Driver) get(ctx context.Context, c *Client, app, id string) (machine, error) {
	var m machine
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id), nil, &m); err != nil {
		return m, fmt.Errorf("reading Machine %s: %w", id, err)
	}
	return m, nil
}

// ensureIPs gives the app Fly's shared IPv4 and an IPv6 address once, so
// its fly.dev address answers.
func (d *Driver) ensureIPs(ctx context.Context, c *Client, app string, logf deploy.Logf) error {
	var got struct {
		IPs []ipAssignment `json:"ips"`
	}
	path := "/v1/apps/" + url.PathEscape(app) + "/ip_assignments"
	if err := c.do(ctx, "GET", path, nil, &got); err != nil {
		return fmt.Errorf("reading the app's addresses: %w", err)
	}
	v4, v6 := false, false
	for _, ip := range got.IPs {
		if strings.Contains(ip.IP, ":") {
			v6 = true
		} else {
			v4 = true
		}
	}
	for _, want := range []struct {
		have bool
		kind string
	}{{v4, "shared_v4"}, {v6, "v6"}} {
		if want.have {
			continue
		}
		if err := c.do(ctx, "POST", path, map[string]any{"type": want.kind}, nil); err != nil {
			return fmt.Errorf("giving the app a %s address: %w", want.kind, err)
		}
		logf("gave the app a %s address", want.kind)
	}
	return nil
}

// retireOthers destroys this deployment's Machines of every other
// generation, by the marks they carry. One it cannot destroy is said.
func (d *Driver) retireOthers(ctx context.Context, c *Client, p deploy.Placement, app string, g int64, logf deploy.Logf) {
	ms, err := c.machines(ctx, app)
	if err != nil {
		logf("the previous release's Machines were not listed, so they still run: %v", err)
		return
	}
	for _, m := range ms {
		if d.ours(m, p) && m.Config.Metadata[metaRole] != "need" && m.Config.Metadata[metaGeneration] != strconv.FormatInt(g, 10) && m.State != "destroyed" {
			d.destroy(ctx, c, app, m.ID, logf)
		}
	}
}

func (d *Driver) destroyAll(ctx context.Context, c *Client, app string, ids map[string]string, logf deploy.Logf) {
	for _, id := range ids {
		d.destroy(ctx, c, app, id, logf)
	}
}

// destroy removes one Machine; a failure is logged with what is left.
func (d *Driver) destroy(ctx context.Context, c *Client, app, id string, logf deploy.Logf) {
	if err := c.do(context.WithoutCancel(ctx), "DELETE", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(id)+"?force=true", nil, nil); err != nil && !isStatus(err, 404) {
		logf("Machine %s was not destroyed and may still run: %v", id, err)
		return
	}
	logf("destroyed Machine %s", id)
}

// witness asks the app's front door what AppTown can witness: the declared
// HTTP readiness path answers, and a static public part serves this
// release's identity. Neither declared leaves health unknown, and the log
// says so; nothing is assumed.
func (d *Driver) witness(ctx context.Context, pl plan, logf deploy.Logf) (bool, error) {
	base := appURL(pl.App)
	witnessed := false
	if pl.Ready != "" {
		if err := d.askFrontDoor(ctx, base+pl.Ready, func(status int, _ []byte) bool { return appcontract.Ready(status, 0) }, "the readiness path"); err != nil {
			return false, err
		}
		logf("the front door answers %s", base+pl.Ready)
		witnessed = true
	}
	if pl.Release != "" {
		release := pl.Release
		if err := d.askFrontDoor(ctx, base+appcontract.ReleasePath, func(status int, body []byte) bool { return appcontract.ServesRelease(status, body, release) }, "release "+release); err != nil {
			return false, terminal(err)
		}
		logf("the front door serves this release: %s answers %s", base+appcontract.ReleasePath, release)
		witnessed = true
	}
	if !witnessed {
		logf("which release the front door serves is not witnessed: the public part declares no HTTP readiness path and is not a static site")
	}
	return witnessed, nil
}

// askFrontDoor asks url up to six times, pollInterval apart, until ok.
func (d *Driver) askFrontDoor(ctx context.Context, rawURL string, ok func(int, []byte) bool, what string) error {
	last := ""
	for try := 0; try < 6; try++ {
		req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
		if err != nil {
			return err
		}
		res, err := d.frontDoor.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(res.Body, appcontract.MaxReleaseAnswer))
			res.Body.Close()
			if readErr == nil && ok(res.StatusCode, body) {
				return nil
			}
			last = fmt.Sprintf("%d %q", res.StatusCode, strs.Truncate(strings.TrimSpace(string(body)), 80))
		} else {
			last = err.Error()
		}
		if err := poll.Wait(ctx, d.options.pollInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("the front door did not answer %s for %s after six tries (last: %s)", rawURL, what, last)
}

// restorePrevious is also recovery's path: the checkpoint precedes
// releasing a volume, so a controller restart retains the old config.
func (d *Driver) restorePrevious(ctx context.Context, c *Client, p deploy.Placement, app string, previous map[string]createMachine, logf deploy.Logf) error {
	ms, err := c.machines(ctx, app)
	if err != nil {
		return err
	}
	var failed []error
	for part, body := range previous {
		found := false
		for _, m := range ms {
			if d.ours(m, p) && m.Config.Metadata[metaRole] == "part" && m.Config.Metadata[metaGroup] == part && m.Config.Metadata[metaGeneration] == body.Config.Metadata[metaGeneration] {
				found = true
				if m.State != "started" && !body.SkipLaunch {
					if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(m.ID)+"/start", nil, nil); err != nil {
						failed = append(failed, err)
					}
				}
			}
		}
		if found {
			continue
		}
		var m machine
		if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/machines", body, &m); err != nil {
			failed = append(failed, fmt.Errorf("restoring previous part %s: %w", part, err))
			continue
		}
		if !body.SkipLaunch {
			if err := d.awaitReady(ctx, c, app, part, m.ID, logf); err != nil {
				failed = append(failed, err)
			}
		}
	}
	return errors.Join(failed...)
}
