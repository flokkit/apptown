package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
)

type Deployments struct {
	features   *identity.Service
	deployment *deployment.Service
	data       *dataprotection.Service
	projects   *project.Projects
}

func NewDeployments(deployment *deployment.Service, data *dataprotection.Service, projects *project.Projects, features *identity.Service) Deployments {
	return Deployments{deployment: deployment, data: data, projects: projects, features: features}
}

// The deployment routes' responses.
type (
	// deploymentChecked records repairs made during a status refresh.
	deploymentChecked struct {
		Deployment string                      `json:"deployment"`
		Checked    []deployment.RecoveryResult `json:"checked"`
		RuntimeObservationView
	}
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
// door. Unknown keys are refused, so a misspelled setting is not ignored.
type DeploymentConfigurationRequest struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	From   string `json:"from"`
	deployment.Attributes
	StartFrom ids.ID `json:"startFrom"`
}

// handleCreateDeployment: POST /projects/{slug}/deployments
// {name, target, feed, audience, data, startFrom?} — project
// deployment settings (§2.7): a standing deployment bound to an offered
// target; startFrom names a copy its first release fills its data from.
func (h Deployments) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.features, w, r, access.CapDeploymentBind, access.Resource{Type: "project", ID: p.ID.String(), ProjectID: p.ID}, access.Context{Consequential: true}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in DeploymentConfigurationRequest) {
		target, err := h.deployment.Targets.ByName(r.Context(), in.Target)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if err := h.features.RequireFeature(r.Context(), deployment.TargetFeatureArea(target.Kind)); err != nil {
			WriteFault(w, err)
			return
		}
		result, err := h.deployment.Configure(r.Context(), deployment.ConfigureDeployment{ProjectID: p.ID, Name: in.Name, Target: in.Target, From: in.From, Attributes: in.Attributes, StartFrom: in.StartFrom})
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
	p, ok := projectFromPath(h.projects, w, r)
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
		writeAdmitted(w, taken)
	})
}

// handleDeployDeployment: POST /projects/{slug}/deployments/{name}/deploy
// {bundleId?, expectedSourceBundleId?} — ship a bundle to a standing deployment whose feed is
// promotion or manual (§4.2). A protected deployment takes the release
// grant. Returns the release number; the release page is the record.
func (h Deployments) DeployDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true, strict: true}, func(in DeployRequest) {
		accepted, err := h.deployment.RequestRelease(r.Context(), deployment.DeploySelection{ProjectID: p.ID, DeploymentName: r.PathValue("name"), ReleaseNumber: in.ReleaseNumber, BeforeRelease: in.BeforeRelease, BundleID: in.BundleID, ExpectedSourceBundleID: in.ExpectedSourceBundleID, AcceptUncopiedData: in.AcceptUncopiedData, AcceptUncheckedCopy: in.AcceptUncheckedCopy})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 202, ReleaseRequested{OK: true, Number: accepted.Number, Deployment: accepted.Deployment, State: accepted.State})
	})
}

// CheckAgain: POST /projects/{slug}/deployments/{name}/check — a person's
// status refresh: AppTown repairs uncertain or contradictory records and refreshes
// its runtime reading (deployment.Service.CheckAgain). It never deploys anything.
func (h Deployments) CheckAgain(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	result, err := h.deployment.CheckAgain(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, deploymentChecked{Deployment: result.Deployment, Checked: result.Checked, RuntimeObservationView: runtimeObservationView(result.Observation)})
}

// AIModels: GET /api/v1/projects/{slug}/deployments/{name}/ai-models — the
// AI models keys the deployment owns and what OpenRouter says of each now.
func (h Deployments) AIModels(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
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
// bundle to release, or none for request-time feed selection. A web promotion
// sends the displayed source bundle as an expectation.
type DeployRequest struct {
	ReleaseNumber          int64  `json:"releaseNumber"`
	BeforeRelease          int64  `json:"beforeRelease"`
	BundleID               ids.ID `json:"bundleId"`
	ExpectedSourceBundleID ids.ID `json:"expectedSourceBundleId"`
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
	p, ok := projectFromPath(h.projects, w, r)
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
	p, ok := projectFromPath(h.projects, w, r)
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
	p, ok := projectFromPath(h.projects, w, r)
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
	p, ok := projectFromPath(h.projects, w, r)
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

type FeedConfirmationRequest struct {
	Revision int64 `json:"revision"`
}

// ConfirmFeed records a person's standing authorization at the revision shown.
func (h Deployments) ConfirmFeed(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in FeedConfirmationRequest) {
		dep, err := h.deployment.ConfirmFeed(r.Context(), p.ID, r.PathValue("name"), in.Revision)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, dep)
	})
}

type FeedsConfirmed struct {
	Confirmed []string `json:"confirmed"`
	Refused   []string `json:"refused"`
}

func (h Deployments) ConfirmAllFeeds(w http.ResponseWriter, r *http.Request) {
	confirmed, refused, err := h.deployment.ConfirmAllFeeds(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, FeedsConfirmed{Confirmed: confirmed, Refused: refused})
}

// SetFeed saves one revision of a deployment's feed settings.
func (h Deployments) SetFeed(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in deployment.FeedSettings) {
		dep, err := h.deployment.SetFeed(r.Context(), p.ID, r.PathValue("name"), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, dep)
	})
}

// Classification reads a deployment's audience and data with the values its
// target and offer allow; ?audience=&data= also asks what saving them would do.
func (h Deployments) Classification(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	var to *deployment.Attributes
	if a, d := r.URL.Query().Get("audience"), r.URL.Query().Get("data"); a != "" || d != "" {
		to = &deployment.Attributes{Audience: a, DataClass: d}
	}
	view, err := h.deployment.Classification(r.Context(), p.ID, r.PathValue("name"), to)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, view)
}

// SetClassification saves a deployment's audience and data.
func (h Deployments) SetClassification(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in deployment.ClassificationSettings) {
		dep, err := h.deployment.SetClassification(r.Context(), p.ID, r.PathValue("name"), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, dep)
	})
}

// controlRuntime changes runtime state through Deployment's checked operation.
func (h Deployments) controlRuntime(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	_, err := h.deployment.ControlRuntime(r.Context(), p.ID, r.PathValue("name"), r.PathValue("action"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, map[string]bool{"ok": true})
}

func (h Deployments) StartRuntime(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("action", "start")
	h.controlRuntime(w, r)
}
func (h Deployments) StopRuntime(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("action", "stop")
	h.controlRuntime(w, r)
}
func (h Deployments) RestartRuntime(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("action", "restart")
	h.controlRuntime(w, r)
}

func (h Deployments) SetRoutineUpdates(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in deployment.RoutineUpdateSettings) {
		out, err := h.deployment.SetRoutineUpdates(r.Context(), p.ID, r.PathValue("name"), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
