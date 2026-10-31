package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
	"github.com/flokkit/apptown/internal/strs"
)

type Infrastructure struct {
	deployment  *deployment.Service
	hosts       *deployment.Hosts
	providers   *deployment.Providers
	projects    *project.Projects
	db          *database.DB
	cache       InventoryCache
	builtinDB   bool
	dbContainer string
	dbVolume    string
	now         func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewInfrastructure(deployment *deployment.Service, hosts *deployment.Hosts, providers *deployment.Providers, projects *project.Projects, db *database.DB, cache InventoryCache, builtinDB bool, dbContainer string, dbVolume string, now func() time.Time) Infrastructure {
	return Infrastructure{deployment: deployment, hosts: hosts, providers: providers, projects: projects, db: db, cache: cache, builtinDB: builtinDB, dbContainer: dbContainer, dbVolume: dbVolume, now: now}
}

// Disks serves the filesystems this AppTown and its worker write to
// (maintenance.Disks).
type Disks struct {
	disks interface {
		Read(context.Context) []diskspace.Reading
	}
}

func NewDisks(disks interface {
	Read(context.Context) []diskspace.Reading
}) Disks {
	return Disks{disks: disks}
}

// DiskReadings is the latest background reading of each filesystem.
type DiskReadings struct {
	Disks []diskspace.Reading `json:"disks"`
}

// Disks: GET /infrastructure/disks — each filesystem this AppTown and its
// worker write to, each with its own recorded reading and time.
func (h Disks) Disks(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, DiskReadings{Disks: h.disks.Read(r.Context())})
}

// InfrastructureTargets is every cloud target with what is there.
type InfrastructureTargets struct {
	Targets []InfrastructureView `json:"targets"`
}

// Infrastructure: GET /infrastructure — every cloud
// target with what is there and what it is hosting. The provider's list
// comes from the cache (infrastructure_cache.go); the records are fresh.
func (h Infrastructure) Infrastructure(w http.ResponseWriter, r *http.Request) {
	ts, err := h.deployment.Targets.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []InfrastructureView{}
	for _, t := range ts {
		if t.Status == deployment.TargetRetired {
			continue // out of service: its page still exists, the fleet view does not list it
		}
		k := deployment.DescribeTarget(t)
		region := deployment.TargetPlace(t)
		domain, _ := t.Ingress["domain"].(string)
		v := InfrastructureView{Target: t.Name, Kind: t.Kind, Family: k.Family, Origin: t.Origin, Status: t.Status.String(), StatusLabel: t.Status.Display(), Facts: targetFacts(t), Domain: domain,
			Vendor: k.Vendor, Place: strings.Join(nonEmpty(k.Title, k.Vendor, region), " · "), Resources: []deploy.Resource{}}
		if v.Resources, err = h.recordedResources(r.Context(), t); err != nil {
			WriteFault(w, err) // an unread record is not an orphan
			return
		}
		recorded := v.Resources
		if _, err := h.providers.TargetInventory(t); errors.Is(err, deploy.ErrNoInventory) {
			v.Unreported = true // unknown, not an error: the records stand alone
		} else if err != nil {
			v.Error = err.Error()
		} else {
			{
				live, shared, errText, at, invalidated := h.cache.Live(r.Context(), t)
				v.Error, v.Invalidated = errText, invalidated
				if !at.IsZero() {
					v.AsOf = stamp.Of(at)
				}
				var answered bool
				v.Resources, v.Live, answered = deployment.InventoryAnswer(v.Resources, live, errText, at, invalidated)
				if answered {
					if t.Kind == deployment.KindDockerHost || t.Kind == deployment.KindPortainer {
						if v.Resources, v.Shared, err = h.attributeLeftovers(r.Context(), t, v.Resources, shared); err != nil {
							// Failed placement is unknown, never an orphan. Keep
							// this target's records while the rest of the fleet
							// remains readable; effects still verify placement.
							v.Error = "Inventory placement could not be verified: " + err.Error()
							v.Resources, v.Live, _ = deployment.InventoryAnswer(recorded, nil, v.Error, time.Time{}, false)
							v.AsOf, v.Shared = stamp.Time{}, ""
						}
					}
				}
			}
		}
		if v.Resources == nil {
			v.Resources = []deploy.Resource{} // never null: the page reads it as a list
		}
		sort.SliceStable(v.Resources, func(i, j int) bool {
			ki, kj := kindRank(v.Resources[i].Kind), kindRank(v.Resources[j].Kind)
			if ki != kj {
				return ki < kj
			}
			return v.Resources[i].Name < v.Resources[j].Name
		})
		out = append(out, v)
	}
	WriteJSON(w, 200, InfrastructureTargets{Targets: out})
}

// RemoveInfrastructure: POST /infrastructure/{target}/remove {kind,
// name, id, confirm} — take away something at a target that AppTown's
// records do not claim: a container or a data volume on a Docker host, a
// stack on a Portainer environment. Refused for anything recorded (an
// environment's things go through the environment's doors) and without the
// resource's name as confirmation; a volume that is in use is refused by
// the daemon. Every removal is an audit row.
func (h Infrastructure) RemoveInfrastructure(w http.ResponseWriter, r *http.Request) {
	t, err := h.deployment.Targets.ByName(r.Context(), r.PathValue("target"))
	if err != nil {
		WriteError(w, 404, "no target named %q", r.PathValue("target"))
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Kind, Name, ID, Confirm string
	}) {
		in.Kind, in.Name, in.ID = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Name), strings.TrimSpace(in.ID)
		if in.Name == "" || strings.TrimSpace(in.Confirm) != in.Name {
			WriteError(w, 400, "confirm must equal the name of what is being removed")
			return
		}
		recorded, err := h.recordedResources(r.Context(), t)
		if err != nil {
			WriteError(w, 503, "not removed: AppTown's records could not be read, so it cannot tell whether %s %s is claimed: %v", in.Kind, in.Name, err)
			return
		}
		for _, rec := range recorded {
			if rec.Kind == in.Kind && (rec.Name == in.Name || (in.ID != "" && rec.ID == in.ID)) {
				WriteError(w, 409, "%s %s is on AppTown's records (%s) — retire its deployment instead", in.Kind, in.Name, hostsWords(rec.Hosts))
				return
			}
		}
		// A removal cannot be undone: it is requested on the record before
		// it happens, and its outcome recorded after.
		what := map[string]any{"kind": in.Kind, "name": in.Name, "id": in.ID}
		admit := func() bool {
			if err := audit.At(clock.Or(h.now)()).Record(r.Context(), h.db, Actor(r), ids.Nil, "infrastructure.remove-requested", t.Name, what); err != nil {
				WriteError(w, 503, "not removed: the request could not be recorded: %v", err)
				return false
			}
			return true
		}
		var out []byte
		switch {
		case t.Kind == deployment.KindDockerHost && (in.Kind == "container" || in.Kind == "volume"):
			inventory, lookupErr := h.providers.TargetInventory(t)
			if lookupErr != nil {
				WriteError(w, 503, "not removed: %v", lookupErr)
				return
			}
			if err := h.requireUnclaimedResource(r.Context(), inventory, in.Kind, in.Name, in.ID); err != nil {
				WriteFault(w, err)
				return
			}
			dk, scopeErr := h.hosts.DockerScope(r.Context(), t)
			if scopeErr != nil {
				WriteError(w, 502, "not removed: %v", scopeErr)
				return
			}
			if !admit() {
				return
			}
			if in.Kind == "container" {
				out, err = dk.Run(r.Context(), "rm", "-f", in.Name)
			} else {
				out, err = dk.Run(r.Context(), "volume", "rm", in.Name)
			}
		case t.Kind == deployment.KindPortainer && in.Kind == "stack":
			cfg, cerr := h.providers.PortainerConfig(r.Context(), t)
			if cerr != nil {
				WriteError(w, 502, "%v", cerr)
				return
			}
			endpointID, eerr := portainer.EndpointID(r.Context(), cfg)
			if eerr != nil {
				WriteError(w, 502, "%v", eerr)
				return
			}
			if in.ID == "" {
				WriteError(w, 400, "a stack is removed by its id")
				return
			}
			// Listing and removal share the already verified destination and
			// credential snapshot; do not resolve another target between them.
			inventory := portainer.Inventory{Config: func(context.Context) (*portainer.Target, error) { return cfg, nil }}
			if err := h.requireUnclaimedResource(r.Context(), inventory, in.Kind, in.Name, in.ID); err != nil {
				WriteFault(w, err)
				return
			}
			if !admit() {
				return
			}
			code, raw, derr := portainer.JSON(r.Context(), cfg, "DELETE", fmt.Sprintf("/api/stacks/%s?endpointId=%d", in.ID, endpointID), nil)
			if derr != nil || code >= 300 {
				err = fmt.Errorf("Portainer refused: HTTP %d %v %s", code, derr, strs.Truncate(string(raw), 200))
			}
		default:
			WriteError(w, 400, "nothing to remove: a %s on a %s target", in.Kind, t.Kind)
			return
		}
		settle := context.WithoutCancel(r.Context())
		if err != nil {
			why := strs.Truncate(strings.TrimSpace(string(out))+" "+err.Error(), 300)
			failed := map[string]any{"kind": in.Kind, "name": in.Name, "id": in.ID, "error": why}
			if recErr := audit.At(clock.Or(h.now)()).Record(settle, h.db, Actor(r), ids.Nil, "infrastructure.remove-failed", t.Name, failed); recErr != nil {
				why += "; and recording that it failed: " + recErr.Error()
			}
			WriteError(w, 502, "not removed: %s", why)
			return
		}
		h.deployment.InfrastructureChanged(t.ID)
		if err := audit.At(clock.Or(h.now)()).Record(settle, h.db, Actor(r), ids.Nil, "infrastructure.removed", t.Name, what); err != nil {
			WriteError(w, 500, "%s %s was removed, but its outcome was not recorded: %v", in.Kind, in.Name, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// requireUnclaimedResource checks fresh provider identity and the same current
// placement claims used by the inventory projection, before a removal receipt.
func (h Infrastructure) requireUnclaimedResource(ctx context.Context, inventory deploy.Inventorier, kind, name, id string) error {
	observed, err := inventory.Inventory(ctx)
	if err != nil {
		return fault.Wrap(fault.Unavailable, fmt.Errorf("not removed: inventory unavailable: %w", err))
	}
	var selected []deploy.Resource
	for _, resource := range observed {
		if resource.Kind == kind && resource.Name == name && (id == "" || resource.ID == id) {
			selected = append(selected, resource)
		}
	}
	if len(selected) != 1 {
		return fault.New(fault.Conflict, "not removed: the exact resource was not observed uniquely; reload the page")
	}
	if slices.Contains(selected[0].Tags, deploy.TagConfirmsOnly) {
		// AppTown owns what carries this installation's tag, nothing else.
		return fault.New(fault.Conflict, fmt.Sprintf("not removed: %s does not carry this installation's mark", name))
	}
	shared, err := h.deployment.Inventory.Shared(ctx, selected)
	if err != nil {
		return fault.Wrap(fault.Unavailable, fmt.Errorf("not removed: placement could not be verified: %w", err))
	}
	if len(shared) != 0 {
		return fault.New(fault.Conflict, fmt.Sprintf("not removed: %s belongs to %s/%s on %s; retire its deployment instead", name, shared[0].Project, shared[0].Deployment, shared[0].Target))
	}
	return nil
}
