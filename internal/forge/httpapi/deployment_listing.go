package httpapi

import (
	"net/http"
	"path/filepath"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

type DeploymentListing struct {
	projects   *project.Projects
	deployment *deployment.Service
	dataDir    string
}

func NewDeploymentListing(projects *project.Projects, deployment *deployment.Service, dataDir string) DeploymentListing {
	return DeploymentListing{projects: projects, deployment: deployment, dataDir: dataDir}
}

// Deployments formats the checked domain listing for the project UI.
func (h DeploymentListing) Deployments(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	summaries, err := h.deployment.List(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	isStatic := false
	if c, err := docker.ReadWorkspaceContract(filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)); err == nil && c.PublicPart().IsStatic() {
		isStatic = true
	}
	out := make([]DeploymentView, 0, len(summaries))
	for _, e := range summaries {
		v := DeploymentView{DeploymentPolicy: e.DeploymentPolicy, Name: e.Name, Kind: e.Kind.String(), URL: e.URL, HostOnly: e.HostOnly, ReleaseRun: e.ReleaseRun, UpdatedAt: e.UpdatedAt, Static: isStatic,
			Feed: e.Feed, Audience: e.Audience, Data: e.DataClass, Protected: e.Protected, Generation: e.Generation,
			Readiness: e.Readiness.String(), ChangeID: e.ChangeID, AIModels: e.AIModelsKeys, Running: e.Observation.State.Running(), RuntimeObservationView: runtimeObservationView(e.Observation)}
		if e.Observation.Err != nil {
			v.ObservationIssue = "Runtime status is unavailable"
		}
		for _, n := range e.Needs {
			v.Needs = append(v.Needs, DeploymentNeedView{Need: n.Need, Name: n.Name, ID: n.ID, Region: n.Region, Size: n.Size, Version: n.Version, Status: n.Status.String()})
		}
		if t := e.Target; t != nil {
			k := deployment.DescribeTarget(&deployment.Target{Kind: t.Kind, Origin: t.Origin})
			v.Target = &DeploymentTargetView{Name: t.Name, Kind: t.Kind, Origin: t.Origin, Title: k.Title, Vendor: k.Vendor, Family: k.Family, Region: t.Region}
		}
		out = append(out, v)
	}
	WriteJSON(w, 200, out)
}

// DeploymentView is one row of GET /projects/{slug}/deployments: a
// deployment as the project's pages show it.
type DeploymentView struct {
	DeploymentPolicy deployment.DeploymentPolicy `json:"deploymentPolicy,omitempty"`
	Name             string                      `json:"name"`
	Kind             string                      `json:"kind"`
	URL              string                      `json:"url"`
	HostOnly         bool                        `json:"hostOnly,omitempty"` // URL answers on the AppTown host only
	Running          *bool                       `json:"running"`
	ReleaseRun       ids.ID                      `json:"releaseRun"`
	UpdatedAt        string                      `json:"updatedAt"`
	Static           bool                        `json:"static,omitempty"`
	Target           *DeploymentTargetView       `json:"target,omitempty"`
	RuntimeObservationView
	Feed       string               `json:"feed,omitempty"`
	Audience   string               `json:"audience,omitempty"`
	Data       string               `json:"data,omitempty"`
	Protected  bool                 `json:"protected"`
	Generation int64                `json:"generation,omitempty"`
	Readiness  string               `json:"readiness,omitempty"`
	ChangeID   ids.ID               `json:"changeId,omitzero"`
	Needs      []DeploymentNeedView `json:"needs,omitempty"` // what backs it: the managed services fulfilled for it
	// AIModels names the AI models keys it owns (read their allowance at
	// .../ai-models).
	AIModels []string `json:"aiModels,omitempty"`
}

// DeploymentNeedView is a managed service fulfilled for a deployment.
type DeploymentNeedView struct {
	Need    string `json:"need"`
	Name    string `json:"name"`
	ID      string `json:"id,omitempty"`
	Region  string `json:"region,omitempty"`
	Size    string `json:"size,omitempty"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status"`
}

// DeploymentTargetView is the deployment target a deployment runs on.
type DeploymentTargetView struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Origin string `json:"origin"`
	Title  string `json:"title"`
	Vendor string `json:"vendor"`
	Family string `json:"family"`
	Region string `json:"region"`
}
