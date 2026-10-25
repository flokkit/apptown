package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
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
func (h Infrastructure) attributeLeftovers(ctx context.Context, t *deployment.Target, res []deploy.Resource) ([]deploy.Resource, string, error) {
	shared, err := h.deployment.Inventory.Shared(ctx, res)
	if err != nil {
		return nil, "", err
	}
	byIndex := make(map[int]deployment.SharedResource)
	var notes []string
	counts := make(map[string]int)
	var order []string
	for _, claim := range shared {
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

// inventoryAnswer lays the cached provider answer over the records. latest
// is whether the latest ask answered; answered whether there is any list
// to lay over them: the latest, or the last good one under a later error
// (the page says how old it is). With no answer yet — the first ask still
// on its way — the records stay as recorded: a record is "missing" only
// when an answer lacks it. Merging no answer called every record of a live
// attached host missing (sweep 7).
func inventoryAnswer(recorded, live []deploy.Resource, errText string, at time.Time) (resources []deploy.Resource, latest, answered bool) {
	latest = errText == "" && !at.IsZero()
	if live == nil && !latest {
		return coalesceInventory(inventorySlots(recorded)), false, false
	}
	return mergeInventory(recorded, live), latest, true
}

// mergeInventory lays the provider's list over the records: a record the
// provider confirms takes the provider's live facts; a record the provider
// has no trace of is "missing"; a provider item with no record is
// "unrecorded".
func mergeInventory(recorded, live []deploy.Resource) []deploy.Resource {
	recorded, live = coalesceInventory(inventorySlots(recorded)), coalesceInventory(live)
	seen := map[string]bool{}
	key := inventoryIdentity
	byKey := map[string]deploy.Resource{}
	byName := map[string][]deploy.Resource{}
	for _, l := range live {
		byKey[key(l)] = l
		name := l.Kind + ":name:" + l.Name
		byName[name] = append(byName[name], l)
	}
	var out []deploy.Resource
	for _, r := range recorded {
		l, ok := byKey[key(r)]
		if !ok {
			// A name can fill a missing identity, never contradict one.
			matches := byName[r.Kind+":name:"+r.Name]
			if len(matches) == 1 && (r.ID == "" || matches[0].ID == "") {
				l, ok = matches[0], true
			}
		}
		if !ok {
			r.Record = deploy.RecordMissing
			out = append(out, r)
			continue
		}
		seen[key(l)] = true
		merged := l
		merged.Tags = slices.DeleteFunc(slices.Clone(l.Tags), func(t string) bool { return t == deploy.TagConfirmsOnly })
		merged.Record = deploy.RecordRecorded
		merged.Hosts = r.Hosts
		merged.Retained = r.Retained
		if merged.URL == "" {
			merged.URL = r.URL
		}
		out = append(out, merged)
	}
	for _, l := range live {
		if seen[key(l)] || l.Kind == "image" || slices.Contains(l.Tags, deploy.TagConfirmsOnly) {
			continue
		}
		if l.Kind != "registry" || l.Record == "" {
			l.Record = deploy.RecordUnrecorded
		}
		out = append(out, l)
	}
	return out
}

// inventorySlots describes named Docker container slots, not each replaced
// incarnation retained by teardown ownership. Shape only the presentation:
// recordedResources also guards removal by immutable ID and must stay intact.
func inventorySlots(recorded []deploy.Resource) []deploy.Resource {
	out := append([]deploy.Resource(nil), recorded...)
	for i := range out {
		if out[i].Kind == "container" && out[i].Name != "" && (out[i].Name != out[i].ID || docker.Named(out[i].Name)) {
			out[i].ID = ""
		}
	}
	return out
}

// inventoryIdentity aliases Docker volume/network names with their name-valued
// IDs. Other provider IDs remain authoritative regardless of display name.
func inventoryIdentity(r deploy.Resource) string {
	named := (r.Kind == "volume" || r.Kind == "network") && r.ID == r.Name
	if r.ID != "" && !named {
		return r.Kind + ":id:" + r.ID
	}
	return r.Kind + ":name:" + r.Name
}

// coalesceInventory shapes the read model only. Ownership receipts remain
// immutable; several claims of one resource retain all distinct attributions.
func coalesceInventory(in []deploy.Resource) []deploy.Resource {
	var out []deploy.Resource
	byKey := map[string]int{}
	for _, r := range in {
		key := inventoryIdentity(r)
		idx, found := byKey[key]
		if !found {
			idx = len(out)
			byKey[key] = idx
			copy := r
			copy.Hosts = nil
			out = append(out, copy)
		}
		to := &out[idx]
		if to.URL == "" {
			to.URL = r.URL
		}
		if to.Retained == nil {
			to.Retained = r.Retained
		}
		for _, host := range r.Hosts {
			exists := false
			for _, old := range to.Hosts {
				if old == host {
					exists = true
					break
				}
			}
			if !exists {
				to.Hosts = append(to.Hosts, host)
			}
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
	Target     string            `json:"target"`
	Kind       string            `json:"kind"`
	Family     string            `json:"family"`
	Origin     string            `json:"origin"`
	Status     string            `json:"status"` // the target's own: ready | unreachable | retired
	Facts      string            `json:"facts"`  // the concrete address, in words
	Domain     string            `json:"domain,omitempty"`
	Place      string            `json:"place"`
	Vendor     string            `json:"vendor"`
	Live       bool              `json:"live"`
	Error      string            `json:"error,omitempty"`
	AsOf       stamp.Time        `json:"asOf,omitzero"` // when the provider last answered
	Refreshing bool              `json:"refreshing"`    // a fresh answer is on its way
	Resources  []deploy.Resource `json:"resources"`
	// Shared: resources at this daemon that belong to another target's
	// stacks on the same verified Docker daemon,
	// left out of this list, in words.
	Shared string `json:"shared,omitempty"`
}

type InventoryCache interface {
	Live(context.Context, *jobs.Queue, string, deploy.Inventorier, bool) ([]deploy.Resource, string, time.Time, bool)
	Invalidate()
}
