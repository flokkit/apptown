package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/internal/strs"
)

type Infrastructure struct {
	deployment  *deployment.Service
	hosts       *deployment.Hosts
	providers   *deployment.Providers
	projects    *project.Projects
	db          *database.DB
	cache       InventoryCache
	queue       *jobs.Queue
	builtinDB   bool
	dbContainer string
	dbVolume    string
	intranet    string
	now         func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
}

func NewInfrastructure(deployment *deployment.Service, hosts *deployment.Hosts, providers *deployment.Providers, projects *project.Projects, db *database.DB, cache InventoryCache, queue *jobs.Queue, builtinDB bool, dbContainer string, dbVolume string, intranet string, now func() time.Time) Infrastructure {
	return Infrastructure{deployment: deployment, hosts: hosts, providers: providers, projects: projects, db: db, cache: cache, queue: queue, builtinDB: builtinDB, dbContainer: dbContainer, dbVolume: dbVolume, intranet: intranet, now: now}
}

// Disks serves the filesystems this forge and its worker write to
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

// DiskReadings is each filesystem, read on the request.
type DiskReadings struct {
	Disks []diskspace.Reading `json:"disks"`
}

// Disks: GET /infrastructure/disks — each filesystem this forge and its
// worker write to, read now, each its own reading.
func (h Disks) Disks(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, DiskReadings{Disks: h.disks.Read(r.Context())})
}

// InfrastructureTargets is every cloud target with what is there.
type InfrastructureTargets struct {
	Targets []InfrastructureView `json:"targets"`
}

// Infrastructure: GET /infrastructure[?refresh=1] — every cloud
// target with what is there and what it is hosting. The provider's list
// comes from the cache (infrastructure_cache.go); the records are fresh.
func (h Infrastructure) Infrastructure(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") != ""
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
		v := InfrastructureView{Target: t.Name, Kind: t.Kind, Family: k.Family, Origin: t.Origin, Status: t.Status.String(), Facts: targetFacts(t), Domain: domain,
			Vendor: k.Vendor, Place: strings.Join(nonEmpty(k.Title, k.Vendor, region), " · "), Resources: []deploy.Resource{}}
		if v.Resources, err = h.recordedResources(r.Context(), t); err != nil {
			WriteFault(w, err) // an unread record is not an orphan
			return
		}
		if inv, err := h.providers.TargetInventory(t); err == nil {
			{
				live, errText, at, refreshing := h.cache.Live(r.Context(), h.queue, t.Name, inv, force)
				v.Error, v.Refreshing = errText, refreshing
				if !at.IsZero() {
					v.AsOf = stamp.Of(at)
				}
				var answered bool
				v.Resources, v.Live, answered = inventoryAnswer(v.Resources, live, errText, at)
				if answered {
					if t.Kind == deployment.KindDockerHost {
						// Without the project list every leftover would read
						// as nobody's, so a failed read fails the page.
						if v.Resources, v.Shared, err = h.attributeLeftovers(r.Context(), t, v.Resources); err != nil {
							WriteFault(w, err)
							return
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
// name, id, confirm} — take away something at a target that the forge's
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
		var out []byte
		switch {
		case t.Kind == deployment.KindDockerHost && (in.Kind == "container" || in.Kind == "volume"):
			dk, scopeErr := h.hosts.DockerScope(r.Context(), t)
			if scopeErr != nil {
				WriteError(w, 502, "not removed: %v", scopeErr)
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
			code, raw, derr := portainer.JSON(r.Context(), cfg, "DELETE", fmt.Sprintf("/api/stacks/%s?endpointId=%d", in.ID, endpointID), nil)
			if derr != nil || code >= 300 {
				err = fmt.Errorf("Portainer refused: HTTP %d %v %s", code, derr, strs.Truncate(string(raw), 200))
			}
		default:
			WriteError(w, 400, "nothing to remove: a %s on a %s target", in.Kind, t.Kind)
			return
		}
		if err != nil {
			WriteError(w, 502, "not removed: %s", strs.Truncate(strings.TrimSpace(string(out))+" "+err.Error(), 300))
			return
		}
		h.cache.Invalidate()
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "infrastructure.removed", t.Name, map[string]any{"kind": in.Kind, "name": in.Name, "id": in.ID})
		WriteJSON(w, 200, Done{OK: true})
	})
}
