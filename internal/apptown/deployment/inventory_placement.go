package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
)

// SharedResource attributes one observed Docker resource or Portainer stack to its current
// deployment on a verified Portainer connection to the same daemon.
type SharedResource struct {
	Index      int
	Target     string
	Project    string
	Deployment string
}

// Shared reads current placement claims without changing the observations.
// A recognizable claim whose scope cannot be verified is unknown, not an orphan.
func (inv *Inventory) Shared(ctx context.Context, resources []deploy.Resource) ([]SharedResource, error) {
	deployments, err := inv.deployments.All(ctx)
	if err != nil {
		return nil, err
	}
	claims := make(map[string][]ProjectDeployment)
	for _, d := range deployments {
		claims[portainer.StackName(portainer.StackNames(d.ID))] = append(claims[portainer.StackName(portainer.StackNames(d.ID))], d)
	}
	verified := make(map[ids.ID]*portainer.Target)
	targets := make(map[ids.ID]*Target)
	var out []SharedResource
	for i, r := range resources {
		if r.Stack == "" {
			continue
		}
		candidates := claims[r.Stack]
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) != 1 {
			return nil, fmt.Errorf("stack %s has ambiguous deployment identity", r.Stack)
		}
		d := candidates[0]
		if inv.targets == nil || inv.providers == nil {
			return nil, fmt.Errorf("deployment placement verification is unavailable")
		}
		target := targets[d.TargetID]
		if target == nil {
			target, err = inv.targets.ByID(ctx, d.TargetID)
			if err != nil {
				return nil, fmt.Errorf("reading %s/%s placement: %w", d.Project, d.Name, err)
			}
			targets[d.TargetID] = target
		}
		binding, err := providerBindingOf(target)
		if err != nil {
			return nil, fmt.Errorf("verifying %s placement: %w", target.Name, err)
		}
		if binding == nil {
			continue
		}
		if r.ObservedDaemonID == "" {
			return nil, fmt.Errorf("stack %s has no observed daemon identity; refresh its inventory", r.Stack)
		}
		cfg := verified[target.ID]
		if cfg == nil {
			cfg, err = inv.providers.PortainerConfig(ctx, target)
			if err != nil {
				return nil, fmt.Errorf("verifying %s placement: %w", target.Name, err)
			}
			verified[target.ID] = cfg
		}
		if cfg.Scope.DaemonID != r.ObservedDaemonID {
			continue
		}
		label := target.DisplayLabel
		if label == "" {
			label = target.Name
		}
		out = append(out, SharedResource{Index: i, Target: label, Project: d.Project, Deployment: d.Name})
	}
	return out, nil
}
