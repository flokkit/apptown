package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Runtime uses immutable container IDs. Only recorded application containers
// are selected; needs, volumes, images, networks and init steps are untouched.
func (d *Driver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	dk, err := d.Scope(ctx)
	if err != nil {
		return err
	}
	selected, err := d.runtimeContainers(ctx, dk, p)
	if err != nil {
		return err
	}
	run := func(verb string) error {
		for _, r := range selected {
			if _, err := dk.Run(ctx, verb, r.ID); err != nil {
				return fmt.Errorf("could not %s an app container: %w", verb, err)
			}
		}
		for _, r := range selected {
			got, err := Inspect(ctx, dk, r.ID)
			if err != nil {
				return err
			}
			if got.ID != r.ID || got.Running != (verb == "start") {
				return fmt.Errorf("the target has not confirmed the app container %s", verb)
			}
		}
		return nil
	}
	switch action {
	case "stop":
		return run("stop")
	case "start":
		return run("start")
	case "restart":
		if err := run("stop"); err != nil {
			return err
		}
		return run("start")
	default:
		return errors.New("unknown runtime action")
	}
}

// runtimeContainers selects the same app containers for acts and confirmation.
func (d *Driver) runtimeContainers(ctx context.Context, dk Scope, p deploy.Placement) ([]Runtime, error) {
	pub, err := Inspect(ctx, dk, p.Container)
	if err != nil {
		return nil, err
	}
	if pub.ID == "" || pub.Labels[dockerlabel.Deployment] != string(p.Label) {
		return nil, errors.New("the app container does not belong to this deployment")
	}
	selected := []Runtime{}
	if d.Owned == nil {
		return nil, errors.New("runtime control requires the deployment's owned containers")
	}
	owned, err := d.Owned(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range owned {
		if o.Kind != "container" || o.Need != "" || o.ID == pub.ID {
			continue
		}
		r, err := Inspect(ctx, dk, o.ID)
		if errors.Is(err, ErrContainerAbsent) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if r.ID != o.ID {
			return nil, errors.New("the app part's identity changed")
		}
		selected = append(selected, r)
	}
	selected = append(selected, pub)
	return selected, nil
}

func (d *Driver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	dk, err := d.Scope(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	containers, err := d.runtimeContainers(ctx, dk, p)
	if err != nil {
		return deploy.Observation{}, err
	}
	for _, c := range containers {
		if c.Running == p.Stopped {
			return deploy.Observation{}, errors.New("the target has not confirmed all app parts reached the requested state")
		}
	}
	rollout := "active"
	if p.Stopped {
		rollout = "stopped"
	}
	return deploy.Observation{Generation: p.Generation, Rollout: rollout}, nil
}
