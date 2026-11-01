package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/stamp"
)

// recordedResources is what AppTown's records claim at a target, with the
// AppTown's own database first when it lives on the built-in host: that one is
// AppTown's, not an orphan. A record that cannot be read is an error, never
// "not claimed": the remove door deletes only what no record claims.
func (h Infrastructure) recordedResources(ctx context.Context, t *deployment.Target) ([]deploy.Resource, error) {
	var out []deploy.Resource
	if t.Origin == deployment.OriginBuiltIn && h.builtinDB {
		own := []deploy.ResourceHost{{Project: "AppTown itself", Deployment: "its database"}}
		out = append(out, deploy.Resource{Kind: "container", Name: h.dbContainer, Record: deploy.RecordRecorded, Hosts: own})
		out = append(out, deploy.Resource{Kind: "volume", Name: h.dbVolume, Record: deploy.RecordRecorded, Hosts: own})
	}
	recorded, err := h.deployment.Inventory.Recorded(ctx, t)
	if err != nil {
		return nil, err
	}
	return append(out, recorded...), nil
}

// attributeLeftovers keeps ordinary teardown evidence and attributes shared
// resources only through the deployment owner's verified current placement.
func (h Infrastructure) attributeLeftovers(ctx context.Context, t *deployment.Target, res []deploy.Resource, shared []deployment.SharedResource) ([]deploy.Resource, string, error) {
	byIndex := make(map[int]deployment.SharedResource)
	var notes []string
	counts := make(map[string]int)
	var order []string
	for _, claim := range shared {
		index := -1
		for i, r := range res {
			if r.Kind == claim.Kind && r.ID == claim.ID && r.Name == claim.Name {
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		claim.Index = index
		if res[claim.Index].Record != deploy.RecordUnrecorded {
			continue
		}
		byIndex[claim.Index] = claim
		label := claim.Target + " (" + claim.Project + "/" + claim.Deployment + ")"
		if counts[label] == 0 {
			order = append(order, label)
		}
		counts[label]++
	}
	for _, label := range order {
		notes = append(notes, fmt.Sprintf("%d resource(s) belong to %s on this same verified Docker daemon; see that environment's inventory.", counts[label], label))
	}
	deployments, err := h.deployment.Deployments.All(ctx)
	if err != nil {
		return nil, "", err
	}
	owners := map[string][2]string{}
	for _, d := range deployments {
		if n, err := d.HostNames(); err == nil {
			owners[n.Stack()] = [2]string{d.Project, d.Name}
			owners[n.App()] = [2]string{d.Project, d.Name}
		}
	}
	var out []deploy.Resource
	for i, r := range res {
		if _, ok := byIndex[i]; ok && r.Record == deploy.RecordUnrecorded {
			continue
		}
		if r.Record == deploy.RecordUnrecorded {
			if d, ok := owners[r.Owner]; ok {
				r.Retained = &deploy.RetainedNote{Project: d[0], Deployment: d[1], Reason: h.leftoverReason(ctx, t, d[0], d[1])}
			}
		}
		out = append(out, r)
	}
	return out, strings.Join(notes, " "), nil
}

// leftoverReason says why a deployment no longer records a resource it
// made: it moved to another target, it stands here without it, or it was
// torn down. Only an answer that the project or deployment is not there
// says torn down; a read that failed says the reason is not known, since
// the sentence stands beside resources an administrator may remove.
func (h Infrastructure) leftoverReason(ctx context.Context, t *deployment.Target, slug, env string) string {
	unknown := func(err error) string {
		return "whether the deployment still stands could not be read: " + err.Error()
	}
	p, err := h.projects.BySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "the deployment was torn down"
	} else if err != nil {
		return unknown(err)
	}
	e, err := h.deployment.Deployments.ByName(ctx, p.ID, env)
	if errors.Is(err, sql.ErrNoRows) {
		return "the deployment was torn down"
	} else if err != nil {
		return unknown(err)
	}
	if e.TargetID != t.ID {
		now, err := h.deployment.Targets.ByID(ctx, e.TargetID)
		if err != nil {
			return "the deployment now runs on another target, which could not be read: " + err.Error()
		}
		return "the deployment now runs on " + now.Name + " and no longer records this"
	}
	return "the deployment no longer records this"
}

func kindRank(k string) int {
	switch k {
	case "app":
		return 0
	case "database":
		return 1
	}
	return 2
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// targetFacts is the concrete address of a target, in words: what a person
// would type to reach the same place.
func targetFacts(t *deployment.Target) string {
	var parts []string
	if t.Origin == deployment.OriginBuiltIn {
		parts = append(parts, "this AppTown's own Docker daemon")
	}
	if u, _ := t.Address["user"].(string); u != "" {
		if h, _ := t.Address["hostname"].(string); h != "" {
			s := "ssh " + u + "@" + h
			if p, ok := t.Address["port"].(float64); ok && int(p) != 22 && p > 0 {
				s += fmt.Sprintf(":%d", int(p))
			}
			parts = append(parts, s)
		}
	}
	if u, _ := t.Address["url"].(string); u != "" {
		parts = append(parts, u)
	}
	if e, _ := t.Address["environment"].(string); e != "" {
		parts = append(parts, "Portainer environment "+e)
	}
	if r, _ := t.Address["region"].(string); r != "" {
		parts = append(parts, "region "+r)
	}
	if t.Provider != "" {
		parts = append(parts, "through the "+t.Provider+" connection")
	}
	return strings.Join(parts, " · ")
}

func hostsWords(hs []deploy.ResourceHost) string {
	var parts []string
	for _, h := range hs {
		parts = append(parts, h.Project+"/"+h.Deployment)
	}
	return strings.Join(parts, ", ")
}

type InfrastructureView struct {
	StatusLabel deployment.TargetLabel `json:"statusLabel"`
	Target      string                 `json:"target"`
	Kind        string                 `json:"kind"`
	Family      string                 `json:"family"`
	Origin      string                 `json:"origin"`
	Status      string                 `json:"status"` // the target's own: ready | unreachable | retired
	Facts       string                 `json:"facts"`  // the concrete address, in words
	Domain      string                 `json:"domain,omitempty"`
	Place       string                 `json:"place"`
	Vendor      string                 `json:"vendor"`
	Unknown     bool                   `json:"unknown,omitempty"`
	Live        bool                   `json:"live"`
	Error       string                 `json:"error,omitempty"`
	// Unreported: the target's kind has no inventory reader, so what it
	// holds beyond the records is unknown (deploy.ErrNoInventory).
	Unreported  bool              `json:"unreported,omitempty"`
	AsOf        stamp.Time        `json:"asOf,omitzero"` // when the provider last answered
	Invalidated bool              `json:"invalidated"`   // the reading predates an AppTown act
	Resources   []deploy.Resource `json:"resources"`
	// Shared: resources at this daemon that belong to another target's
	// stacks on the same verified Docker daemon,
	// left out of this list, in words.
	Shared string `json:"shared,omitempty"`
}

type InventoryCache interface {
	Live(context.Context, *deployment.Target) ([]deploy.Resource, []deployment.SharedResource, string, time.Time, bool, bool)
}
