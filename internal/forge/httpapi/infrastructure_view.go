package httpapi

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/stamp"
)

// recordedResources is what the forge's records claim at a target, with the
// forge's own database first when it lives on the built-in host: that one is
// the forge's, not an orphan. A record that cannot be read is an error, never
// "not claimed": the remove door deletes only what no record claims.
func (h Infrastructure) recordedResources(ctx context.Context, t *deployment.Target) ([]deploy.Resource, error) {
	var out []deploy.Resource
	if t.Origin == deployment.OriginBuiltIn && h.builtinDB {
		forge := []deploy.ResourceHost{{Project: "AppTown itself", Deployment: "its database"}}
		out = append(out, deploy.Resource{Kind: "container", Name: h.dbContainer, Record: deploy.RecordRecorded, Hosts: forge})
		out = append(out, deploy.Resource{Kind: "volume", Name: h.dbVolume, Record: deploy.RecordRecorded, Hosts: forge})
	}
	recorded, err := h.deployment.Inventory.Recorded(ctx, t)
	if err != nil {
		return nil, err
	}
	return append(out, recorded...), nil
}

// attributeLeftovers reads the labels on a Docker host's unrecorded
// resources. One made by the forge for a deployment (forge.owner) is
// "left behind" by that deployment, with the reason, and stays removable.
// One that belongs to an intranet stack (the compose project label names
// a project's stack) is the intranet target's, not this host's: the
// intranet's Portainer environment runs on this same daemon, so it is left
// out here and said in words.
func (h Infrastructure) attributeLeftovers(ctx context.Context, t *deployment.Target, res []deploy.Resource) ([]deploy.Resource, string, error) {
	slugs, err := h.projectSlugs(ctx)
	if err != nil {
		return nil, "", err
	}
	_, noIntranet := h.deployment.Targets.ByName(ctx, h.intranet)
	var out []deploy.Resource
	shared := 0
	for _, r := range res {
		if r.Record != deploy.RecordUnrecorded {
			out = append(out, r)
			continue
		}
		if _, ok := stackProject(r.Stack, slugs); ok && noIntranet == nil && t.Name != h.intranet {
			shared++
			continue
		}
		if slug, env, ok := docker.OwnerDeployment(r.Owner, slugs); ok {
			r.Retained = &deploy.RetainedNote{Project: slug, Deployment: env, Reason: h.leftoverReason(ctx, t, slug, env)}
		}
		out = append(out, r)
	}
	note := ""
	if shared > 0 {
		word := "resources here belong"
		if shared == 1 {
			word = "resource here belongs"
		}
		note = fmt.Sprintf("%d %s to the intranet's stacks: its Portainer environment runs on this same daemon. It is on the intranet's page.", shared, word)
	}
	return out, note, nil
}

// projectSlugs: every project's slug, longest first so a slug that is a
// prefix of another (app, app-api) never wins by accident.
func (h Infrastructure) projectSlugs(ctx context.Context) ([]string, error) {
	out, err := project.SlugsIn(ctx, h.db)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out, nil
}

// leftoverReason says why a deployment no longer records a resource it
// made: it moved to another target, it stands here without it, or it was
// torn down.
func (h Infrastructure) leftoverReason(ctx context.Context, t *deployment.Target, slug, env string) string {
	p, err := h.projects.BySlug(ctx, slug)
	if err != nil {
		return "the deployment was torn down"
	}
	e, err := h.deployment.Deployments.ByName(ctx, p.ID, env)
	if err != nil {
		return "the deployment was torn down"
	}
	if e.TargetID != t.ID {
		if now, err := h.deployment.Targets.ByID(ctx, e.TargetID); err == nil {
			return "the deployment now runs on " + now.Name + " and no longer records this"
		}
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
		if out[i].Kind == "container" && out[i].Name != "" && (out[i].Name != out[i].ID || strings.HasPrefix(out[i].Name, "forge-")) {
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

// stackProject: which project an intranet compose stack belongs to.
func stackProject(stack string, slugs []string) (string, bool) {
	if stack == "" {
		return "", false
	}
	for _, s := range slugs {
		if stack == portainer.StackName(s) {
			return s, true
		}
	}
	return "", false
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
	// stacks (the intranet's Portainer environment on the same daemon),
	// left out of this list, in words.
	Shared string `json:"shared,omitempty"`
}

type InventoryCache interface {
	Live(context.Context, *jobs.Queue, string, deploy.Inventorier, bool) ([]deploy.Resource, string, time.Time, bool)
	Invalidate()
}
