package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
)

type Tiers struct {
	projects     *project.Projects
	integrations *integration.Service
	deployment   *deployment.Service
	previews     *preview.Service
	work         *workflow.Service
	db           *database.DB
	dataDir      string
}

func NewTiers(projects *project.Projects, integrations *integration.Service, deployment *deployment.Service, previews *preview.Service, work *workflow.Service, db *database.DB, dataDir string) Tiers {
	return Tiers{projects: projects, integrations: integrations, deployment: deployment, previews: previews, work: work, db: db, dataDir: dataDir}
}

// Tiers: GET /api/v1/tiers — the runtime picture of the projects the
// caller can read; another project's apps are left out, not refused.
func (h Tiers) Tiers(w http.ResponseWriter, r *http.Request) {
	// ?project=<slug> scopes the map to one project (the tier map is
	// per-project for now; the instance-wide fleet view is this same
	// endpoint without the filter, when some instance grows a crowd). A
	// named project the caller cannot read is refused.
	var onlyID ids.ID
	if slug := r.URL.Query().Get("project"); slug != "" {
		p, err := h.projects.Readable(r.Context(), slug)
		if err != nil {
			WriteFault(w, err)
			return
		}
		onlyID = p.ID
	}
	projects, err := h.visible(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}

	preview := TierView{Name: "preview", Title: "Preview", Onward: "accept → land", Apps: []TierApp{},
		Gloss: "one per change, while it's being worked on — dies with the change"}
	if live, err := h.previews.LiveChangePreviews(r.Context()); err != nil {
		preview.Observed, preview.Reason = previewsUnobserved(err)
	} else {
		changes, err := previewChanges(r.Context(), h.work, live)
		if err != nil {
			WriteFault(w, err)
			return
		}
		for _, pv := range live {
			change := changes[pv.ChangeID]
			if change == nil {
				continue
			}
			projID, title := change.ProjectID, change.Title
			// A readable change is not a readable project: the tier map
			// shows a preview only in a project the caller can read.
			p := projects[projID]
			if p == nil || (!onlyID.IsZero() && projID != onlyID) {
				continue
			}
			app := TierApp{Detail: fmt.Sprintf("change %d: %s", change.Number, strs.Truncate(title, 50)), Running: true,
				Slug: p.Slug, Name: p.Name, Icon: p.Icon, ChangeID: change.ID, ChangeNumber: change.Number}
			preview.Apps = append(preview.Apps, app)
		}
	}

	main := TierView{Name: "main", Title: "Main", Onward: "release", Apps: []TierApp{},
		Gloss: "the standing copy of each project's main branch — where the team tests"}
	envs, err := h.deployment.Deployments.Named(r.Context(), "main")
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	for _, e := range envs {
		p := projects[e.ProjectID]
		if p == nil || (!onlyID.IsZero() && p.ID != onlyID) {
			continue
		}
		app := h.runtimeApp(r.Context(), p, &e.Deployment)
		repository, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if c, err := docker.ReadWorkspaceContract(repository); err == nil && c.PublicPart().IsStatic() {
			app.Static = true
		}
		main.Apps = append(main.Apps, app)
	}

	relEnv, err := h.releaseDeployment(r.Context())
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	lan := TierView{Name: relEnv, Title: "Intranet", Onward: "someday", Apps: []TierApp{},
		Gloss: "released for everyone on the intranet — survives reboots, lives on the platform"}
	releaseDeployments, err := h.deployment.Deployments.Named(r.Context(), relEnv)
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	for _, e := range releaseDeployments {
		p := projects[e.ProjectID]
		if p == nil || (!onlyID.IsZero() && p.ID != onlyID) {
			continue
		}
		lan.Apps = append(lan.Apps, h.runtimeApp(r.Context(), p, &e.Deployment))
	}

	net := TierView{Name: "net", Title: "Internet", Future: true, Apps: []TierApp{},
		Gloss: "available to the world — not built yet"}

	WriteJSON(w, 200, TierMap{Tiers: []TierView{preview, main, lan, net}})
}

// runtimeApp projects the current placement, independently of release history.
// The caller filters project visibility before reaching or observing its target.
func (h Tiers) runtimeApp(ctx context.Context, p *project.Project, d *deployment.Deployment) TierApp {
	reach := h.deployment.Reach(ctx, p.Slug, d)
	app := TierApp{DeploymentPolicy: d.UpdatePolicy(), Slug: p.Slug, Name: p.Name, Icon: p.Icon, URL: reach.URL, HostOnly: reach.HostOnly}
	switch o := h.deployment.ObserveContainer(ctx, d); o.State {
	case deployment.ContainerUnavailable, deployment.ContainerUnknown:
		app.Observed = o.State.String()
	default:
		app.Running = o.State == deployment.ContainerRunning
	}
	return app
}

// Departures: GET /api/v1/tiers/departures — movement through the
// tiers, newest first: every release (to main by landings, to the intranet
// by the button, failed attempts included) plus the previews alive right
// now. The departures board reading of the same world the tier map draws.
func (h Tiers) Departures(w http.ResponseWriter, r *http.Request) {
	relEnv, err := h.releaseDeployment(r.Context())
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	// The destination is a deployment, named with its kind: a bare "MAIN"
	// read as the branch (sweep 7).
	tierName := func(env string) string {
		switch env {
		case relEnv:
			return "intranet deployment"
		default:
			return env + " deployment"
		}
	}
	projects, err := h.visible(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	recent, err := h.deployment.RecentReleases(r.Context(), 60)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []Departure{}
	for _, r := range recent {
		p := projects[r.ProjectID]
		if p == nil {
			continue
		}
		d := Departure{At: r.At(), Slug: p.Slug, Name: p.Name, Icon: p.Icon, Who: r.ApprovedBy, State: r.State.String(),
			Tier: tierName(r.Deployment), Label: fmt.Sprintf("release #%d", r.Number)}
		if r.State == deployment.Released && strings.HasPrefix(r.Notes, "http") {
			d.URL = r.Notes
		}
		out = append(out, d)
	}
	board := Departures{}
	if live, err := h.previews.LiveChangePreviews(r.Context()); err != nil {
		board.PreviewsObserved, board.PreviewsReason = previewsUnobserved(err)
	} else {
		changes, err := previewChanges(r.Context(), h.work, live)
		if err != nil {
			WriteFault(w, err)
			return
		}
		for _, pv := range live {
			change := changes[pv.ChangeID]
			if change == nil {
				continue
			}
			p := projects[change.ProjectID]
			if p == nil {
				continue
			}
			d := Departure{At: pv.StartedAt, Tier: "preview", Slug: p.Slug, Name: p.Name, Icon: p.Icon,
				Label: fmt.Sprintf("preview for change %s#%d", p.Slug, change.Number), State: "live"}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	board.Departures = out
	WriteJSON(w, 200, board)
}

type TierApp struct {
	DeploymentPolicy deployment.DeploymentPolicy `json:"deploymentPolicy,omitempty"`
	Slug             string                      `json:"slug"`
	Name             string                      `json:"name"`
	Icon             string                      `json:"icon,omitempty"`
	Detail           string                      `json:"detail,omitempty"`
	URL              string                      `json:"url,omitempty"`
	// HostOnly: URL answers on the AppTown host only (deployment.Reach).
	HostOnly bool `json:"hostOnly,omitempty"`
	Running  bool `json:"running"`
	// Observed is why Running was not observed: "unavailable" (the
	// built-in host of an AppTown with no local Docker) or "unknown".
	Observed string `json:"observed,omitempty"`
	Static   bool   `json:"static,omitempty"`
	// ChangeID is the change a preview is of, and ChangeNumber its number
	// within the project; absent on other tiers.
	ChangeID     ids.ID `json:"changeId,omitzero"`
	ChangeNumber int64  `json:"changeNumber,omitempty"`
}

// TierMap is the tiers an app passes through, with what is on each.
type TierMap struct {
	Tiers []TierView `json:"tiers"`
}

// Departures is the board of what reached which tier, newest first.
// PreviewsObserved is why the previews alive now are not on it, when they
// could not be listed: "unavailable" or "unknown", with PreviewsReason.
type Departures struct {
	Departures       []Departure `json:"departures"`
	PreviewsObserved string      `json:"previewsObserved,omitempty"`
	PreviewsReason   string      `json:"previewsReason,omitempty"`
}

// Departure is one arrival on a tier.
type Departure struct {
	At   string `json:"at"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
	// Tier is where it went: the deployment's name with its kind ("main
	// deployment", "intranet deployment"), or "preview".
	Tier  string `json:"tier"`
	Label string `json:"label"`
	Who   string `json:"who,omitempty"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

type TierView struct {
	Name   string    `json:"name"`
	Title  string    `json:"title"`
	Gloss  string    `json:"gloss"`
	Onward string    `json:"onward,omitempty"` // the promotion verb toward the next tier
	Future bool      `json:"future,omitempty"`
	Apps   []TierApp `json:"apps"`
	// Observed is why Apps was not observed, when it could not be:
	// "unavailable" (no place previews run that this AppTown has: no local
	// Docker and no worker) or "unknown" (that place did not answer), with
	// Reason. Apps is then empty, which says nothing about what runs.
	Observed string `json:"observed,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// previewsUnobserved words why the previews alive now could not be listed.
func previewsUnobserved(err error) (observed, reason string) {
	if errors.Is(err, docker.ErrNoLocalBackend) {
		return "unavailable", "previews run on a worker or this AppTown's own Docker, and this AppTown has neither"
	}
	return "unknown", err.Error()
}

// Preview metadata is one scoped Work batch, independent of container count.
func previewChanges(ctx context.Context, work *workflow.Service, live []preview.LiveChange) (map[ids.ID]*workflow.Change, error) {
	idList := make([]ids.ID, 0, len(live))
	for _, p := range live {
		idList = append(idList, p.ChangeID)
	}
	records, err := work.ChangeRecords(ctx, ids.Nil, idList)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]*workflow.Change, len(records))
	for _, change := range records {
		out[change.ID] = change
	}
	return out, nil
}

// visible is the projects the caller can read, by ID: the tier map and the
// departures board show nothing of any other project.
func (h Tiers) visible(ctx context.Context) (map[ids.ID]*project.Project, error) {
	rows, err := h.projects.Visible(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]*project.Project, len(rows))
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// releaseDeployment is the intranet's release deployment: the one its
// settings name, "lan" when it is not wired up or names none. Settings that
// cannot be read are an error, never "lan".
func (h Tiers) releaseDeployment(ctx context.Context) (string, error) {
	t, err := h.integrations.PortainerTarget(ctx)
	if errors.Is(err, integration.ErrIntranetNotWired) {
		return "lan", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the intranet's settings: %w", err)
	}
	if t.ReleaseDeployment == "" {
		return "lan", nil
	}
	return t.ReleaseDeployment, nil
}
