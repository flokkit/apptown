package apptown

import (
	"context"
	"errors"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
)

func attentionFor(a *App) *attention.Service {
	return attention.New(access.Checked{Authority: a.Access, Projects: a.Projects}, attention.Sources{
		Integrations: attention.Integrations{Deployments: a.Deployment, Service: a.Integrations, Bindings: a.Integrations.Bindings, Attachments: a.Integrations.Attachments},
		Checks:       attention.Checks{Conformance: a.conformance},
		Workers:      attention.Workers{Observer: a.workerObserver},
		Disks:        attention.Disks{Observer: a.diskObserver},
		Capacity:     attention.Capacity{Targets: a.targets, Observer: a.capacityObserver, Now: a.cfg.clock.Now},
		Inventory:    &attention.Inventory{Targets: a.targets, Records: a.Deployment.Inventory, Observer: attentionInventory{cache: a.inventory, providers: a.targetProviders}, Now: a.cfg.clock.Now},
		Deployments:  attention.Deployments{Projects: a.Projects, Runtime: a.Deployment, Now: a.cfg.clock.Now},
		Protection:   attention.Protection{Projects: a.Projects, Deployments: a.deployments, Observer: a.protectionObserver, Now: a.cfg.clock.Now},
	})
}

type attentionInventory struct {
	cache     *deployment.InventoryReadings
	providers *deployment.Providers
}

func (s attentionInventory) Snapshot(ctx context.Context, t *deployment.Target) (attention.InventoryReading, error) {
	if _, err := s.providers.TargetInventory(t); errors.Is(err, deploy.ErrNoInventory) {
		// No reader to ask: unknown, and nothing is requested.
		return attention.InventoryReading{Unknown: true, Unreported: true}, nil
	}
	got, err := s.cache.SnapshotForTarget(ctx, t)
	return attention.InventoryReading{Items: got.Items, Missing: got.Missing, FailedAt: got.FailedAt, Reason: got.Reason, ObservedAt: got.ObservedAt, AttemptedAt: got.AttemptedAt, Unknown: got.Unknown, Stale: got.Stale, Invalidated: got.Invalidated}, err
}
