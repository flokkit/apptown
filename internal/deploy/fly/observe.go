package fly

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/flokkit/apptown/internal/deploy"
)

// Observe reads the deployment's Machines from one answer of Fly's and says
// what they are, keeping unknown apart from stopped: a Machine whose host
// Fly cannot reach is unknown, never stopped or healthy, and an answer Fly
// did not give is an error, not an empty app. Parts is each part's and
// need's Machine from the same answer.
func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	o := deploy.Observation{Generation: p.Generation, Rollout: "absent", Health: "unknown", URL: p.URL, Handle: p.Handle}
	app, _ := p.Handle["appName"].(string)
	appID, _ := p.Handle["appId"].(string)
	if app == "" {
		return o, nil
	}
	c, _, err := d.client(ctx)
	if err != nil {
		return o, err
	}
	var got flyApp
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app), nil, &got); err != nil {
		if isStatus(err, 404) {
			return o, nil
		}
		return o, err
	}
	if got.ID != appID {
		// Another app now holds the name: this deployment's app is gone.
		return o, nil
	}
	ms, err := c.machines(ctx, app)
	if err != nil {
		return o, err
	}
	o.URL = appURL(app)
	g, _ := handleGeneration(p.Handle)
	o.Rollout, o.Health = state(d.current(ms, p, g), expectedParts(p.Handle))
	o.Parts = d.parts(ms, p)
	if o.Rollout == "active" || o.Rollout == "failed" {
		o.Running = generationWord(g)
	}
	return o, nil
}

// current is the deployment's part Machines of generation g.
func (d *Driver) current(ms []machine, p deploy.Placement, g int64) []machine {
	var out []machine
	for _, m := range ms {
		if d.ours(m, p) && m.Config.Metadata[metaRole] == "part" && m.Config.Metadata[metaGeneration] == strconv.FormatInt(g, 10) {
			out = append(out, m)
		}
	}
	return out
}

// state maps a generation's part Machines to AppTown's rollout and health
// (design/subsystems/deploy-targets.md, "Fly Machines"), judged against the
// parts the generation recorded: a part with no Machine has failed, and a
// generation that recorded no part set is not known to be whole.
func state(ms []machine, expected []string) (rollout, health string) {
	if len(ms) == 0 {
		return "absent", "unknown"
	}
	rollout, health = "active", "healthy"
	have := map[string]bool{}
	for _, m := range ms {
		have[m.Config.Metadata[metaGroup]] = true
	}
	if expected == nil {
		health = "unknown"
	}
	for _, part := range expected {
		if !have[part] {
			rollout, health = "failed", "unhealthy"
		}
	}
	for _, m := range ms {
		if m.HostStatus == "unknown" || m.HostStatus == "unreachable" {
			// Fly cannot reach the host: what the Machine does is not known.
			health = "unknown"
			continue
		}
		switch m.State {
		case "created", "creating", "starting", "updating", "replacing", "restarting":
			if rollout == "active" {
				rollout = "rolling"
			}
			if health == "healthy" {
				health = "unknown"
			}
		case "started":
			if !checksPass(m) && health == "healthy" {
				health = "unhealthy"
			}
		default:
			// stopped, suspended, failed, launch_failed, destroyed: nothing on
			// this target asks for a stop, so a part not running has failed.
			rollout, health = "failed", "unhealthy"
		}
	}
	return rollout, health
}

// Runtime restarts each part's Machine of the running generation. Fly
// cannot keep an app stopped as this target runs it, so start and stop are
// refused before they reach here (runtime controls).
func (d *Driver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	if action != "restart" {
		return terminal(fmt.Errorf("this Fly target does not keep an app stopped yet; %s is not offered", action))
	}
	c, _, err := d.client(ctx)
	if err != nil {
		return err
	}
	app, _ := p.Handle["appName"].(string)
	if app == "" {
		return errors.New("the deployment records no Fly app")
	}
	ms, err := c.machines(ctx, app)
	if err != nil {
		return err
	}
	g, _ := handleGeneration(p.Handle)
	parts := d.current(ms, p, g)
	if len(parts) == 0 {
		return errors.New("Fly lists none of this release's Machines to restart")
	}
	for _, m := range parts {
		if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/machines/"+url.PathEscape(m.ID)+"/restart", nil, nil); err != nil {
			return fmt.Errorf("restarting Machine %s: %w", m.ID, err)
		}
	}
	return nil
}

// ObserveRuntime confirms every part Machine of the running generation is
// started; an unassessable one is an error, never confirmation.
func (d *Driver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	o, err := d.Observe(ctx, p)
	if err != nil {
		return o, err
	}
	if o.Rollout != "active" {
		return o, fmt.Errorf("the app's Machines read %s, not started", o.Rollout)
	}
	if o.Health == "unknown" {
		return o, errors.New("Fly cannot say what one of the app's Machines is doing")
	}
	return o, nil
}
