package deployment

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"slices"
	"time"
)

// InventoryAnswer lays a cached provider answer over the records. latest says
// the latest check answered; answered says there is a list to compare, possibly
// the last good one under a later error. Before the first answer or while the
// reading predates an act, show records alone: that evidence cannot establish
// missing or unrecorded resources.
func InventoryAnswer(recorded, live []deploy.Resource, errText string, at time.Time, invalidated bool) (resources []deploy.Resource, latest, answered bool) {
	latest = errText == "" && !at.IsZero()
	if invalidated || (live == nil && !latest) {
		return coalesceInventory(inventorySlots(recorded)), false, false
	}
	return MergeInventory(recorded, live), latest, true
}

// MergeInventory lays the provider's list over the records: a record the
// provider confirms takes the provider's live facts; a record the provider
// has no trace of is "missing"; a provider item with no record is
// "unrecorded".
func MergeInventory(recorded, live []deploy.Resource) []deploy.Resource {
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
