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

// Declarations reads a project's contract; the second result is false
// when the project has none that can be read.
type Declarations interface {
	Declared(context.Context, ids.ID) (Declared, bool)
}

// Inventory is the records' side of what is at a target.
type Inventory struct {
	deployments *Deployments
	needs       NeedStore
	retained    *Retained
	targets     *Targets
	providers   *Providers
	declared    Declarations
}

func NewInventory(db *database.DB, declared Declarations) *Inventory {
	return &Inventory{deployments: NewDeployments(db), needs: NewNeedStore(db, nil), retained: NewRetained(db), declared: declared}
}

// Recorded is what AppTown's own records say is at a target: the app
// behind every deployment bound to it (a preview's too), the services and
// data volume its contract declares on a Docker host, the database behind
// every managed need instance on a cloud target, and what was retained.
// Only a true orphan then reads unrecorded.
func (inv *Inventory) Recorded(ctx context.Context, t *Target) ([]deploy.Resource, error) {
	var out []deploy.Resource
	envs, err := inv.deployments.OnTarget(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("reading the deployments on %s: %w", t.Name, err)
	}
	for _, e := range envs {
		host := deploy.ResourceHost{Project: e.Project, Deployment: e.Name, Generation: e.ObservedGeneration}
		switch t.Kind {
		case KindDockerHost:
			// What it is called on the host is what it was made with.
			names, err := e.HostNames()
			if err != nil {
				return nil, fmt.Errorf("naming %s/%s on its host: %w", e.Project, e.Name, err)
			}
			name := e.Container
			if name == "" {
				name = names.App()
			}
			out = append(out, deploy.Resource{Kind: "container", Name: name, URL: e.URL, Status: e.Readiness.String(), Record: deploy.RecordRecorded, Hosts: []deploy.ResourceHost{host}})
			if d, ok := inv.declared.Declared(ctx, e.ProjectID); ok {
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
			owned, err := ownedIn(ctx, deploymentsql.New(inv.deployments.db.SQL()), e.ID)
			if err != nil {
				return nil, fmt.Errorf("reading what %s/%s owns: %w", e.Project, e.Name, err)
			}
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
			// AppTown does not list the store, so nothing reads unrecorded.
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
		insts, err := inv.needs.ForDeployment(ctx, e.ID)
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s's needs: %w", e.Project, e.Name, err)
		}
		for _, n := range insts {
			if n.Mode != "managed" {
				continue
			}
			r := databaseResource(n.Handle)
			r.Status = n.Status.String()
			r.Hosts = []deploy.ResourceHost{{Project: e.Project, Deployment: e.Name, Need: n.Need}}
			out = append(out, r)
		}
		// An object store's key, which the account lists; its bucket, which
		// the account's API does not, is named with it.
		owned, err := inv.deployments.Owned(ctx, e.ID)
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s's owned list: %w", e.Project, e.Name, err)
		}
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
				Hosts: []deploy.ResourceHost{{Project: e.Project, Deployment: e.Name, Need: o.Need}}})
		}
	}
	retained, err := inv.retained.OpenForTarget(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("reading what was left at %s: %w", t.Name, err)
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
