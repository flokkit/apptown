package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
)

func attentionFor(a *App) *attention.Service {
	authority := access.Checked{Authority: a.Access, Projects: a.Projects}
	hosts := a.Deployment
	return attention.New(authority, attention.Sources{
		Records:          attentionRecords{projects: a.Projects, deployments: a.Deployment},
		Notifications:    a.Notifications,
		Scans:            attention.Scans{Work: a.Work},
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

type attentionRecords struct {
	projects    *project.Projects
	deployments *deployment.Service
}

func (s attentionRecords) Prepare(ctx context.Context, projectIDs []ids.ID, instance bool) (context.Context, error) {

	projects, err := s.projects.ByIDs(ctx, projectIDs)
	if err != nil {
		return ctx, err
	}
	records := attention.RequestRecords{Projects: projects}
	if instance {
		records.Targets, err = s.deployments.Targets.All(ctx)
		if err != nil {
			return ctx, err
		}
		targetIDs := make([]ids.ID, 0, len(records.Targets))
		for _, t := range records.Targets {
			targetIDs = append(targetIDs, t.ID)
		}
		placements, err := s.deployments.Deployments.OnTargets(ctx, targetIDs)
		if err != nil {
			return ctx, err
		}
		ctx, err = s.deployments.WithHostSamples(ctx, records.Targets, placements)
		if err != nil {
			return ctx, err
		}
	}
	return attention.WithRecords(ctx, records), nil

}
