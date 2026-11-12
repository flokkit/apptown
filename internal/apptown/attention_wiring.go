package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/deployment"
)

func attentionFor(a *App) *attention.Service {
	authority := access.Checked{Authority: a.Access, Projects: a.Projects}
	hosts := attention.Hosts{Authority: authority, Observer: a.Deployment}
	return attention.New(authority, attention.Sources{
		Scans:            attention.Scans{Work: a.Work, Projects: a.Projects},
		ReleaseApprovals: attention.Approvals{Releases: a.Deployment, Now: a.cfg.clock.Now},
		Integrations:     attention.Integrations{Deployments: a.Deployment, Service: a.Integrations, Bindings: a.Integrations.Bindings, Attachments: a.Integrations.Attachments},
		Checks:           attention.Checks{Conformance: a.conformance},
		Workers:          attention.Workers{Observer: a.workerObserver},
		Disks:            attention.Disks{Observer: a.diskObserver},
		Capacity:         attention.Capacity{Hosts: hosts, Targets: a.targets, Observer: a.capacityObserver, Now: a.cfg.clock.Now},
		Inventory:        &attention.Inventory{Hosts: hosts, Targets: a.targets, Records: a.Deployment.Inventory, Observer: attentionInventory{service: a.Deployment}, Now: a.cfg.clock.Now},
		Deployments:      attention.Deployments{Hosts: hosts, Projects: a.Projects, Runtime: a.Deployment, Now: a.cfg.clock.Now},
		Protection:       attention.Protection{Hosts: hosts, Projects: a.Projects, Deployments: a.deployments, Observer: a.protectionObserver, Now: a.cfg.clock.Now},
	})
}

type attentionInventory struct {
	service *deployment.Service
}

func (s attentionInventory) Snapshot(ctx context.Context, t *deployment.Target) (attention.InventoryReading, error) {
	got, err := s.service.InventoryFor(ctx, t)
	return attention.InventoryReading{Items: got.Items, Missing: got.Missing, FailedAt: got.FailedAt, Reason: got.Reason, LastFailure: got.LastFailure, ObservedAt: got.ObservedAt, AttemptedAt: got.AttemptedAt, Unknown: got.Unknown, Stale: got.Stale, Invalidated: got.Invalidated, Unreported: got.Unreported}, err
}
