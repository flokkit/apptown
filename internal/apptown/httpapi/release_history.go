package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type ReleaseHistory struct {
	projects   *project.Projects
	work       *workflow.Service
	deployment *deployment.Service
	db         *database.DB
	backups    *dataprotection.Backups
}

func NewReleaseHistory(projects *project.Projects, work *workflow.Service, deployment *deployment.Service, backups *dataprotection.Backups, db *database.DB) ReleaseHistory {
	return ReleaseHistory{projects: projects, work: work, deployment: deployment, backups: backups, db: db}
}

// ReleaseListed is one release in a project's list, with the titles of the
// Changes new since its deployment's previous successful release.
type ReleaseListed struct {
	DeploymentLabel string                     `json:"deploymentLabel"`
	Decision        deployment.ReleaseDecision `json:"decision"`
	StatusLabel     deployment.ReleaseLabel    `json:"statusLabel"`
	deployment.Release
	Changes []ReleaseChangeTitle `json:"changes"`
	// Serving: the release is its deployment's live one (Releases.Live)
	// and that deployment is observed running right now. "Released" is a
	// record; "live" on the page must mean serving, not a landing on a
	// starter nothing runs.
	Serving bool `json:"serving"`
	// Observed is "unavailable" or "unknown", with Reason, when the
	// release's deployment could not be observed, so Serving false is not
	// "not serving" (deployment.ObserveContainer).
	Observed string `json:"observed,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// SbomArtifact: the software bill of materials AppTown produced
	// last before this release was cut (syft at the landing's source
	// scan) — evidence of what shipped, one click from the release.
	SbomArtifact ids.ID `json:"sbomArtifact,omitzero"`
}

// ReleaseChangeTitle names a Change a release carries.
type ReleaseChangeTitle struct {
	Number int64  `json:"number"`
	ID     ids.ID `json:"id"`
	Title  string `json:"title"`
}

// ListReleases: GET /api/v1/projects/{slug}/releases — newest first,
// with the titles of the new Changes in each release.
func (h ReleaseHistory) ListReleases(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	releases, err := h.deployment.Releases.ForProject(r.Context(), p.ID, 0)
	if err != nil {
		WriteFault(w, err)
		return
	}
	deployments, err := h.deployment.Deployments.ForProject(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	labels := map[string]string{}
	for _, d := range deployments {
		labels[d.Name] = d.DisplayLabel
	}
	out := []ReleaseListed{}
	for _, rel := range releases {
		decision, err := h.deployment.ReleaseDecision(r.Context(), rel)
		if err != nil {
			WriteFault(w, err)
			return
		}
		out = append(out, ReleaseListed{DeploymentLabel: labels[rel.Deployment], Release: *rel, Decision: decision, StatusLabel: rel.State.Display()})
	}
	idList := make([]ids.ID, 0, len(out))
	for _, release := range out {
		idList = append(idList, release.ID)
	}
	contents, err := h.work.ReleaseChanges(r.Context(), p.ID, idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	newChanges, err := h.deployment.Releases.NewChanges(r.Context(), p.ID, idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	served, unobserved, err := h.servedReleases(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		// Serving: the release is what its deployment serves now, by the
		// owner's one rule (Releases.Live, which the release's own page
		// reads: the newest released on a deployment that still stands,
		// matched by its id), and that deployment is observed running. A
		// promotion has no landing run, and an App Platform deployment
		// records no commit: matching those called a release its page
		// called live "not serving".
		if s, running := served[out[i].Deployment]; running && (out[i].DeploymentID.IsZero() || out[i].DeploymentID == s.ID) {
			if out[i].Serving, err = h.deployment.Releases.Live(r.Context(), &out[i].Release); err != nil {
				WriteFault(w, err)
				return
			}
		}
		if o, ok := unobserved[out[i].Deployment]; ok && !out[i].Serving {
			out[i].Observed, out[i].Reason = o.State.String(), o.Reason
		}
		if out[i].SbomArtifact, err = h.sbomBefore(r.Context(), p.ID, out[i].CreatedAt); err != nil {
			WriteFault(w, err)
			return
		}
		// Non-nil even when empty: a release carrying no changes (e.g. a lan
		// promotion) must serialize changes as [], not null — null crashed
		// the Overview after the first intranet release.
		out[i].Changes = []ReleaseChangeTitle{}
		for _, change := range contents[out[i].ID] {
			if !newChanges[out[i].ID][change.ID] {
				continue
			}
			out[i].Changes = append(out[i].Changes, ReleaseChangeTitle{Number: change.Number, ID: change.ID, Title: change.Title})
		}
	}
	WriteJSON(w, 200, out)
}

// ReleaseDetail: GET /projects/{slug}/releases/{number} — the
// PERMALINK. A release is an institutional event; this page is its record:
// who, when, which commit, which bytes (digest), which changes it carried.
func (h ReleaseHistory) ReleaseDetail(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	num, err := pathNumber(r, "number")
	if err != nil {
		WriteError(w, 400, "bad release number")
		return
	}
	rel, err := h.deployment.Releases.ByNumber(r.Context(), p.ID, num)
	if err != nil {
		WriteError(w, 404, "no release #%d on this project", num)
		return
	}
	// The changes a release carries are Work's labels over Delivery's
	// membership receipts, read through Work's door for the verified caller.
	labels, err := h.work.ReleaseChanges(r.Context(), p.ID, []ids.ID{rel.ID})
	if err != nil {
		WriteFault(w, err)
		return
	}
	changes := []ReleaseChange{}
	for _, c := range labels[rel.ID] {
		changes = append(changes, ReleaseChange{ID: c.ID, Title: c.Title, Type: c.Type})
	}
	live, err := h.deployment.Releases.Live(r.Context(), rel)
	if err != nil {
		WriteFault(w, err)
		return
	}
	// The log: every timestamped line the release wrote as it worked — the
	// page IS the record of what happened, success or failure.
	logged, err := h.deployment.Releases.Events(r.Context(), rel.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	events := append([]deployment.ReleaseEvent{}, logged...)
	if rel.State == deployment.ReleaseFailed && len(events) > 0 {
		rel.Failure = events[len(events)-1].Text
	}
	deployments, err := h.deployment.Deployments.ForProject(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	label := rel.Deployment
	for _, d := range deployments {
		if d.Name == rel.Deployment {
			label = d.DisplayLabel
		}
	}
	out := ReleaseDetail{DeploymentLabel: label, Release: rel, StatusLabel: rel.State.Display(), Changes: changes, IsLive: live, Events: events}
	if out.ReplacedBy, err = h.deployment.Releases.ReplacedBy(r.Context(), rel); err != nil {
		WriteFault(w, err)
		return
	}
	if !rel.RollbackOf.IsZero() {
		previous, e := h.deployment.Releases.ByID(r.Context(), rel.RollbackOf)
		if e != nil {
			WriteFault(w, e)
			return
		}
		out.RollbackNumber = previous.Number
	}
	out.Decision, err = h.deployment.ReleaseDecision(r.Context(), rel)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out.Request, err = h.deployment.Releases.RequestMetadata(r.Context(), rel.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}

	if !rel.BackupID.IsZero() {
		out.Backup, err = h.backups.Reference(r.Context(), p.ID, rel.BackupID)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	if out.Request != nil && len(out.Request.BackupEvidence) > 0 {
		var evidence struct {
			Copy struct {
				Reason string `json:"reason"`
			} `json:"copy"`
		}
		if err := json.Unmarshal(out.Request.BackupEvidence, &evidence); err != nil {
			WriteFault(w, err)
			return
		}
		out.BackupReason = evidence.Copy.Reason
	}
	// The bundle's form and images, so a person can see before promoting
	// whether the release can go where only images by digest go.
	if !rel.BundleID.IsZero() {
		b, err := h.deployment.Bundles.Find(r.Context(), rel.BundleID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if b != nil {
			out.Bundle = &ReleaseBundleView{ID: b.ID, ByDigest: b.ByDigest(), Registry: b.BuiltFrom.Registry, Platform: b.BuiltFrom.Platform, Parts: map[string]string{}, Init: map[string]string{}}
			maps.Copy(out.Bundle.Parts, b.Parts)
			maps.Copy(out.Bundle.Init, b.Init)
		}
	}
	// The bill of materials of what this release carried, when there is one.
	if out.SBOMArtifact, err = h.sbomBefore(r.Context(), rel.ProjectID, rel.CreatedAt); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// ReleaseDetail is one release: what it is, the changes it carried, whether
// it is what the deployment serves now, and its log.
type ReleaseDetail struct {
	DeploymentLabel string                          `json:"deploymentLabel"`
	RollbackNumber  int64                           `json:"rollbackNumber,omitempty"`
	Decision        deployment.ReleaseDecision      `json:"decision"`
	Request         *deployment.RequestMetadata     `json:"request,omitempty"`
	BackupReason    string                          `json:"backupReason,omitempty"`
	Backup          *dataprotection.BackupReference `json:"backup,omitempty"`
	StatusLabel     deployment.ReleaseLabel         `json:"statusLabel"`
	Release         *deployment.Release             `json:"release"`
	Changes         []ReleaseChange                 `json:"changes"`
	IsLive          bool                            `json:"isLive"`
	// ReplacedBy is the later release that replaced this one on its
	// deployment; zero when none did.
	ReplacedBy   int64                     `json:"replacedBy,omitempty"`
	Events       []deployment.ReleaseEvent `json:"events"`
	SBOMArtifact ids.ID                    `json:"sbomArtifact,omitzero"`
	Bundle       *ReleaseBundleView        `json:"bundle,omitempty"`
}

// ReleaseBundleView is the bundle a release ships, by its form. By digest,
// each image is <repository>@sha256:<hex> in the project's registry, built
// for platform (os/arch), and any target running that platform can take it,
// App Platform and Portainer included. Otherwise each is an image ID that
// exists only on the Docker daemon that built it.
type ReleaseBundleView struct {
	ID       ids.ID            `json:"id"`
	ByDigest bool              `json:"byDigest"`
	Registry string            `json:"registry,omitempty"`
	Platform string            `json:"platform,omitempty"`
	Parts    map[string]string `json:"parts"`
	Init     map[string]string `json:"init"`
}

// ReleaseChange is a change a release carried, by its label.
type ReleaseChange struct {
	ID    ids.ID `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

// servedReleases says which of a project's deployments run right now, by
// name, each observed by the deployment owner on its own target. A release
// serves only on its own deployment: the same commit running on another
// deployment, or on none after its deployment was taken down, does not make
// it serving. Unobserved: by name, the deployments that could not be
// observed, and why.
func (h ReleaseHistory) servedReleases(ctx context.Context, projectID ids.ID) (served map[string]*deployment.Deployment, unobserved map[string]deployment.ContainerObservation, err error) {
	deployments, err := h.deployment.Deployments.ForProject(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	served, unobserved = map[string]*deployment.Deployment{}, map[string]deployment.ContainerObservation{}
	for _, e := range deployments {
		switch o := h.deployment.ObserveContainer(ctx, e); o.State {
		case deployment.ContainerRunning:
			served[e.Name] = e
		case deployment.ContainerUnavailable, deployment.ContainerUnknown:
			unobserved[e.Name] = o
		}
	}
	return
}

// sbomBefore is the newest SBOM artifact the project's runs produced at or
// before a moment: the bill of materials of what a release cut then
// carried. 0 when none; an error when the moment or the read is not
// known, so a failure never reads as "no bill of materials".
func (h ReleaseHistory) sbomBefore(ctx context.Context, projectID ids.ID, at string) (ids.ID, error) {
	before, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return ids.Nil, fmt.Errorf("reading the release time %q: %w", at, err)
	}
	return workflow.LatestSBOMBeforeIn(ctx, h.db, projectID, before)
}

// ListReleaseBundles supplies the deployment's chosen-release picker.
func (h ReleaseHistory) ListReleaseBundles(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	bundles, err := h.deployment.Bundles.ForProject(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, bundles)
}
