package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
)

// The inventory (design/subsystems/deployment.md §14) is what is at a
// target, in the same words whatever the provider: what AppTown's
// records say should be there, laid under what the provider reports.
// This file is the records' side; the provider's side is each driver's.

// Declared is what a project's contract says its app keeps and needs at a
// target: the records for a Docker host are derived from it.
type Declared struct {
	KeepsData bool     // a named volume survives redeploys
	Needs     []string // the services the app needs (postgres, redis, …)
}

// declarations reads a project's contract; the second result is false
// when the project has none that can be read.
type declarations interface {
	Declared(context.Context, ids.ID) (Declared, bool)
}

// Inventory is the records' side of what is at a target.
type Inventory struct {
	deployments *Deployments
	targets     *Targets
	providers   *Providers
	declared    declarations
}

func newInventory(db *database.DB, declared declarations) *Inventory {
	return &Inventory{deployments: NewDeployments(db), declared: declared}
}

// Recorded is what AppTown's own records say is at a target: the app
// behind every deployment bound to it (a preview's too), the services and
// data volume a standing contract declares on a Docker host, the resources
// a preview boot recorded creating, the database behind
// every managed need instance on a cloud target, and what was retained.
// Only a true orphan then reads unrecorded.
func (inv *Inventory) Recorded(ctx context.Context, t *Target) ([]deploy.Resource, error) {
	records, err := inv.RecordedTargets(ctx, []*Target{t})
	return records[t.ID], err
}

// RecordedTargets derives records for precisely the targets in this request.
func (inv *Inventory) RecordedTargets(ctx context.Context, targets []*Target) (map[ids.ID][]deploy.Resource, error) {
	targetIDs := make([]ids.ID, 0, len(targets))
	for _, t := range targets {
		targetIDs = append(targetIDs, t.ID)
	}
	placements, err := inv.deployments.OnTargets(ctx, targetIDs)
	if err != nil {
		return nil, err
	}
	deploymentIDs := []ids.ID{}
	for _, t := range targets {
		for _, d := range placements[t.ID] {
			deploymentIDs = append(deploymentIDs, d.ID)
		}
	}
	q := deploymentsql.New(inv.deployments.db.SQL())
	owned, err := ownedByDeployments(ctx, q, deploymentIDs)
	if err != nil {
		return nil, err
	}
	needs, err := (NeedStore{db: inv.deployments.db}).forDeployments(ctx, deploymentIDs)
	if err != nil {
		return nil, err
	}
	retainedRows, err := NewRetained(inv.deployments.db).list(ctx, deploymentsql.RetainedResourcesParams{TargetsOnly: true, TargetIds: targetIDs, Disposition: RetainedOpen.String()})
	if err != nil {
		return nil, err
	}
	retained := map[ids.ID][]RetainedResource{}
	for _, r := range retainedRows {
		retained[r.TargetID] = append(retained[r.TargetID], r)
	}
	declarations := map[ids.ID]Declared{}
	declared := func(id ids.ID) Declared {
		if d, ok := declarations[id]; ok {
			return d
		}
		d, _ := inv.declared.Declared(ctx, id)
		declarations[id] = d
		return d
	}
	out := make(map[ids.ID][]deploy.Resource, len(targets))
	for _, t := range targets {
		records, err := inventoryRecords(t, placements[t.ID], owned, needs, retained[t.ID], declared)
		if err != nil {
			return nil, err
		}
		out[t.ID] = records
	}
	return out, nil
}

func inventoryRecords(t *Target, envs []ProjectDeployment, ownership map[ids.ID][]deploy.Owned, needs map[ids.ID][]NeedInstance, retained []RetainedResource, declared func(ids.ID) Declared) ([]deploy.Resource, error) {
	var out []deploy.Resource
	for _, e := range envs {
		host := deploy.ResourceHost{ProjectID: e.ProjectID, DeploymentID: e.ID, Project: e.Project, Deployment: e.Name, DeploymentLabel: inventoryDeploymentLabel(&e.Deployment), Generation: e.ObservedGeneration}
		switch t.Kind {
		case KindDockerHost:
			if e.Kind == preview {
				// The preview boot supplies its resources. Its deployment key
				// never names a stack, and today's contract is not its boot.
				out = append(out, deploy.Resource{Kind: "container", Name: e.Container, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
			} else if e.Container != "" {
				// A standing deployment without a container has not booted one
				// (container_observation.go): nothing of it is on the host yet,
				// so none of its names is a missing resource there.
				// What it is called on the host is what it was made with.
				names, err := e.HostNames()
				if err != nil {
					return nil, fmt.Errorf("naming %s/%s on its host: %w", e.Project, e.Name, err)
				}
				out = append(out, deploy.Resource{Kind: "container", Name: e.Container, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
				d := declared(e.ProjectID)
				if d.KeepsData {
					out = append(out, deploy.Resource{Kind: "volume", Name: names.DataVolume(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
				}
				for _, need := range d.Needs {
					h := host
					h.Need = need
					out = append(out, deploy.Resource{Kind: "container", Name: names.Service(need), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{h}})
				}

			}
			// What the deployment recorded creating is its own: the
			// infrastructure page never offers it for removal by name.
			owned := ownership[e.ID]
			for _, o := range owned {
				h := host
				h.Need = o.Need
				name := o.Name
				if name == "" {
					name = o.ID
				}
				out = append(out, deploy.Resource{Kind: o.Kind, Name: name, ID: o.ID, Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{h}})
			}
			continue
		case KindS3Bucket:
			// The deployment's own bucket, recorded when it was created; the
			// store's buckets marked as this installation's are laid over it.
			if bucket, _ := e.Handle["bucket"].(string); bucket != "" {
				out = append(out, deploy.Resource{Kind: "bucket", Name: bucket, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
			}
			continue
		case KindPortainer:
			if stack, _ := e.Handle["stack"].(string); stack != "" {
				out = append(out, deploy.Resource{Kind: "stack", Name: stack, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
			}
			continue
		}
		appID, _ := e.Handle["appId"].(string)
		appName, _ := e.Handle["appName"].(string)
		if appID == "" {
			continue
		}
		out = append(out, deploy.Resource{Kind: "app", Name: appName, ID: appID, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
		insts := needs[e.ID]
		for _, n := range insts {
			if n.Mode != "managed" {
				continue
			}
			r := databaseResource(n.Handle)
			r.Status = n.Status.String()
			r.Hosts = []deploy.ResourceHost{{ProjectID: e.ProjectID, DeploymentID: e.ID, Project: e.Project, Deployment: e.Name, DeploymentLabel: inventoryDeploymentLabel(&e.Deployment), Need: n.Need}}
			out = append(out, r)
		}
		// An object store's key, which the account lists; its bucket, which
		// the account's API does not, is named with it.
		owned := ownership[e.ID]
		for _, o := range owned {
			if o.Kind != digitalocean.KindSpacesKey {
				continue
			}
			detail := ""
			for _, b := range owned {
				if b.Kind == digitalocean.KindBucket && b.Need == o.Need {
					detail = "for the bucket " + b.ID + ", which this inventory does not list"
				}
			}
			out = append(out, deploy.Resource{Kind: digitalocean.KindSpacesKey, Name: o.Name, ID: o.ID, Detail: detail, Record: deploy.RecordRecorded,
				Hosts: []deploy.ResourceHost{{ProjectID: e.ProjectID, DeploymentID: e.ID, Project: e.Project, Deployment: e.Name, DeploymentLabel: inventoryDeploymentLabel(&e.Deployment), Need: o.Need}}})
		}
	}
	for _, r := range retained {
		// A retained database is a resource of the provider's inventory.
		// Something else a teardown left (an image repository) is not
		// listed there on its own (the registry's detail names its
		// repositories), so it is shown on the retained list only, never
		// as missing here.
		if kind, _ := r.Handle["kind"].(string); kind != "" && kind != "database" {
			continue
		}
		res := databaseResource(r.Handle)
		res.Retained = &deploy.RetainedNote{ID: r.ID, Project: r.Project, Deployment: r.Deployment, RetainedBy: r.RetainedBy, RetainedAt: r.RetainedAt}
		out = append(out, res)
	}
	return out, nil
}

// databaseResource reads a managed database's record from the handle its
// provider gave: the name it has, or the one that was requested before it
// had one.
func databaseResource(h map[string]any) deploy.Resource {
	id, _ := h["id"].(string)
	name, _ := h["name"].(string)
	if name == "" {
		if req, ok := h["requested"].(map[string]any); ok {
			name, _ = req["name"].(string)
		}
	}
	region, _ := h["region"].(string)
	size, _ := h["size"].(string)
	version, _ := h["version"].(string)
	return deploy.Resource{Kind: "database", Name: name, ID: id, Region: region, Size: size, Version: version, Record: deploy.RecordRecorded}
}

// Resource ownership is displayed separately from its machine address.
func inventoryDeploymentLabel(d *Deployment) string {
	if d.Kind == preview && d.DisplayLabel == d.Name {
		return "Preview"
	}
	return d.DisplayLabel
}
