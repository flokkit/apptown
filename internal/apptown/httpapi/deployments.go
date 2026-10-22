package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
)

type Deployments struct {
	deployment *deployment.Service
	data       *dataprotection.Service
	projects   *project.Projects
}

func NewDeployments(deployment *deployment.Service, data *dataprotection.Service, projects *project.Projects) Deployments {
	return Deployments{deployment: deployment, data: data, projects: projects}
}

// The deployment routes' responses.
type (
	// DeploymentRemoved is what a take-down did: what it deleted, the final
	// backup taken before the data went, and what it left at the target
	// (data kept where there is no backup, and deletes that failed).
	//
	// An App Platform deployment that holds data is taken down by an
	// operation instead (final copy, check, then the cluster): the answer
	// is then 202 with OperationAdmitted.
	DeploymentRemoved struct {
		OK          bool                     `json:"ok"`
		Deleted     []string                 `json:"deleted"`
		FinalBackup *dataprotection.FinalSet `json:"finalBackup,omitempty"`
		Orphans     []string                 `json:"orphans"`
	}
	// DeploymentChecked is what Check again observed and settled.
	DeploymentChecked = deployment.CheckResult
	// DeploymentAIModels is each AI models key a deployment owns, with its
	// allowance and spend at OpenRouter now, or why that is unknown.
	DeploymentAIModels struct {
		Keys []deployment.AIModelsKey `json:"keys"`
	}
	// ReleaseRequested is a release accepted for a deployment.
	ReleaseRequested struct {
		OK         bool                    `json:"ok"`
		Number     int64                   `json:"number"`
		Deployment string                  `json:"deployment"`
		State      deployment.ReleaseState `json:"state"`
	}
)

// DeploymentConfigurationRequest selects settings on the existing configuration
// door. Only compatible-update can execute; null/empty/omitted use that default.
// protected-maintenance is recognized but refused, before settings or effects.
type DeploymentConfigurationRequest struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	deployment.Attributes
	StartFrom        ids.ID                      `json:"startFrom"`
	DeploymentPolicy deployment.DeploymentPolicy `json:"deploymentPolicy"`
}

// handleCreateDeployment: POST /projects/{slug}/deployments
// {name, target, feed, audience, data, promoteAfter, startFrom?, deploymentPolicy?} — project
// deployment settings (§2.7): a standing deployment bound to an offered
// target; startFrom names a copy its first release fills its data from.
func (h Deployments) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in DeploymentConfigurationRequest) {
		result, err := h.deployment.Configure(r.Context(), deployment.ConfigureDeployment{ProjectID: p.ID, Name: in.Name, Target: in.Target, Attributes: in.Attributes, StartFrom: in.StartFrom, DeploymentPolicy: in.DeploymentPolicy})
		if err != nil {
			WriteFault(w, err)
			return
		}
		// Preserve the HTTP contract: incompatible settings were saved. The domain
		// result distinguishes this from a failed configuration command.
		if result.ContractChecked && result.ContractIssue != "" {
			WriteError(w, 422, "%s", result.ContractIssue)
			return
		}
		WriteJSON(w, 200, result.Deployment)
	})
}

// handleDeleteDeployment: DELETE /projects/{slug}/deployments/{name}
// {withoutFinalCopy?: <name>} — take-down: the deployment's data is backed
// up, then everything AppTown created for it is deleted; what stays is
// reported. withoutFinalCopy, naming the deployment, is the person's choice
// to take down one whose final copy cannot be taken now, keeping its data.
func (h Deployments) DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		WithoutFinalCopy string `json:"withoutFinalCopy"`
	}) {
		taken, err := h.data.TakeDown(r.Context(), dataprotection.RemoveDeployment{ProjectID: p.ID, Name: r.PathValue("name"), WithoutFinalCopy: in.WithoutFinalCopy})
		if err != nil {
			WriteFault(w, err)
			return
		}
		if taken.Operation != nil {
			WriteAdmitted(w, taken.Operation)
			return
		}
		result := *taken.Removed
		deleted := result.Deleted
		if deleted == nil {
			deleted = []string{}
		}
		WriteJSON(w, 200, DeploymentRemoved{OK: true, Deleted: deleted, FinalBackup: result.FinalBackup, Orphans: result.Retained})
	})
}

// handleDeployDeployment: POST /projects/{slug}/deployments/{name}/deploy
// {bundleId?} — ship a bundle to a standing deployment whose feed is
// promotion or manual (§4.2). A protected deployment takes the release
// grant. Returns the release number; the release page is the record.
func (h Deployments) DeployDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true, strict: true}, func(in DeployRequest) {
		accepted, err := h.deployment.RequestRelease(r.Context(), deployment.DeploySelection{ProjectID: p.ID, DeploymentName: r.PathValue("name"), BundleID: in.BundleID, AcceptUncopiedData: in.AcceptUncopiedData, AcceptUncheckedCopy: in.AcceptUncheckedCopy})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 202, ReleaseRequested{OK: true, Number: accepted.Number, Deployment: accepted.Deployment, State: accepted.State})
	})
}

// MoveNames: POST /projects/{slug}/deployments/{name}/move-names — release
// what the deployment runs, moving it from the names it was made with to
// its names by key first (deployment/move_names.go). Not promised: it goes
// once live and the demo are moved (REMOVE-AFTER resource move).
func (h Deployments) MoveNames(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	accepted, err := h.deployment.RequestRelease(r.Context(), deployment.DeploySelection{ProjectID: p.ID, DeploymentName: r.PathValue("name"), MoveNames: true})
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 202, ReleaseRequested{OK: true, Number: accepted.Number, Deployment: accepted.Deployment, State: accepted.State})
}

// CheckAgain: POST /projects/{slug}/deployments/{name}/check — a person's
// repair of a deployment that needs attention: AppTown observes it on its
// target and settles what it observes (deployment.Service.CheckAgain). It
// never deploys anything.
func (h Deployments) CheckAgain(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	result, err := h.deployment.CheckAgain(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, DeploymentChecked(result))
}

// AIModels: GET /api/v1/projects/{slug}/deployments/{name}/ai-models — the
// AI models keys the deployment owns and what OpenRouter says of each now.
func (h Deployments) AIModels(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	keys, err := h.deployment.AIModels(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, DeploymentAIModels{Keys: keys})
}

// DeployRequest is POST /projects/{slug}/deployments/{name}/deploy: the
// bundle to release, or none for the latest landing's.
type DeployRequest struct {
	BundleID ids.ID `json:"bundleId"`
	// AcceptUncopiedData is a person's choice to release onto a deployment
	// holding data AppTown cannot copy now; the release records it.
	AcceptUncopiedData bool `json:"acceptUncopiedData,omitempty"`
	// AcceptUncheckedCopy is a person's choice to fill a deployment that
	// starts from a copy, on its first release, from a copy an item of
	// which its last check did not show to come back; the release records it.
	AcceptUncheckedCopy bool `json:"acceptUncheckedCopy,omitempty"`
}

// DeploymentResources is GET /projects/{slug}/deployments/{name}/resources:
// the profile and each part's limits the running generation applied, its
// operator's override, and its memory use now.
func (h Deployments) DeploymentResources(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.deployment.Resources(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// DeploymentDiagnostics is GET /projects/{slug}/deployments/{name}/diagnostics:
// the settled generation's runtime facts, part by part, its limit events and
// any crash-loop hold (deployment.Service.Diagnostics).
func (h Deployments) DeploymentDiagnostics(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.deployment.Diagnostics(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// DeploymentLog is GET /projects/{slug}/deployments/{name}/logs?part=: one
// part's recent log, read on this request only (deployment.Service.RecentLog).
// The part is a name the owner checks against the running generation; no
// container is named here.
func (h Deployments) DeploymentLog(w http.ResponseWriter, r *http.Request) {
	// A log can hold the app's secrets: no browser or proxy keeps it.
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.deployment.RecentLog(r.Context(), p.ID, r.PathValue("name"), r.URL.Query().Get("part"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// ResourceOverrideRequest is PUT (set) or DELETE (clear) on
// /projects/{slug}/deployments/{name}/resources: an operator's override of
// one part's resources, with the reason. Memory is written as app.yaml
// writes it (512M, 2G); cpu as a number.
type ResourceOverrideRequest struct {
	Part   string  `json:"part"`
	Memory string  `json:"memory,omitempty"`
	CPU    float64 `json:"cpu,omitempty"`
	Reason string  `json:"reason"`
}

// SetResourceOverride sets an operator's override and redeploys the
// running bundle so it takes effect (deployment.Service.SetOverride).
func (h Deployments) SetResourceOverride(w http.ResponseWriter, r *http.Request) {
	h.resourceOverride(w, r, true)
}

// ClearResourceOverride clears a part's override and redeploys.
func (h Deployments) ClearResourceOverride(w http.ResponseWriter, r *http.Request) {
	h.resourceOverride(w, r, false)
}

func (h Deployments) resourceOverride(w http.ResponseWriter, r *http.Request, set bool) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in ResourceOverrideRequest) {
		change := deployment.OverrideChange{ProjectID: p.ID, Deployment: r.PathValue("name"), Part: in.Part, Reason: in.Reason}
		if set {
			if in.Memory == "none" {
				change.MemoryUnlimited = true
			} else if in.Memory != "" {
				m, err := appcontract.ParseMemory(in.Memory)
				if err != nil {
					WriteError(w, 400, "%v", err)
					return
				}
				change.Resources.MemoryBytes = m
			}
			if in.CPU != 0 {
				c, err := appcontract.ParseCPU(in.CPU)
				if err != nil {
					WriteError(w, 400, "%v", err)
					return
				}
				change.Resources.CPUs = c
			}
			if !change.Resources.Declared() && !change.MemoryUnlimited {
				WriteError(w, 400, "an override sets memory, cpu or both; to remove one, clear it")
				return
			}
		}
		out, err := h.deployment.SetOverride(r.Context(), change)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
