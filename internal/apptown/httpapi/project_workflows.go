package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type ProjectWorkflows struct {
	projects *project.Projects
	adoption *adoption.Service
	work     *workflow.Service
	db       *database.DB
}

func NewProjectWorkflows(projects *project.Projects, adoption *adoption.Service, work *workflow.Service, db *database.DB) ProjectWorkflows {
	return ProjectWorkflows{projects: projects, adoption: adoption, work: work, db: db}
}

// ProjectWorkflowRow is one workflow a project can run, with its plain
// reading (workflow_summary.go) and usage, so lists and the read page lead
// with what the workflow does, not its YAML.
type ProjectWorkflowRow struct {
	WorkflowView
	Summary   *workflowSummary `json:"summary,omitempty"`
	RunsCount int              `json:"runsCount"`
	LastRunAt string           `json:"lastRunAt,omitempty"`
	System    bool             `json:"system"` // the apptown runs it; never a person's pick
	Tier      string           `json:"tier"`   // core | extra | system
	// Withdrawn: stock the running release no longer ships; supplied by a pack.
	Withdrawn bool `json:"withdrawn,omitempty"`
}

func (h ProjectWorkflows) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// Present available library and pack workflows at their latest versions.
	available, err := h.adoption.AvailableWorkflows(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []ProjectWorkflowRow{}
	for _, wf := range available {
		out = append(out, ProjectWorkflowRow{WorkflowView: WorkflowView{ProjectID: p.ID, Name: wf.Name, Kind: wf.Doc.Spec.Kind, Version: wf.Version, YAML: wf.YAML, Origin: wf.OriginRef()}, Withdrawn: wf.Withdrawn})
	}
	usage, err := h.work.WorkflowUsage(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		out[i].Summary = summarizeWorkflow(out[i].YAML)
		out[i].Tier = "extra"
		if d, err := schema.ParseWorkflowStored([]byte(out[i].YAML)); err == nil {
			out[i].System = d.Metadata.System
			out[i].Tier = d.Metadata.EffectiveTier()
		}
		if u, ok := usage[out[i].Name]; ok {
			out[i].RunsCount, out[i].LastRunAt = u.Runs, u.LastRunAt
		}
	}
	WriteJSON(w, 200, out)
}

// ProjectLanes lists runnable library and pack workflows, with system jobs separately.
func (h ProjectWorkflows) ProjectLanes(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	usage, err := workflow.RunCountsByWorkflowIn(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	library, err := h.adoption.AvailableWorkflows(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	lanes, system := []LaneView{}, []LaneView{}
	for _, e := range library {
		l := LaneView{Name: e.Name, Description: e.Doc.Metadata.Description, Tier: e.Doc.Metadata.EffectiveTier(),
			Origin: e.Origin, Version: e.Version, Deprecated: e.Doc.Metadata.Deprecated, //lint:ignore SA1019 reading the deprecated flag is the point
			Withdrawn: e.Withdrawn, RunsCount: usage[e.Name], Summary: summarizeWorkflow(e.YAML)}
		if e.Doc.Metadata.System {
			system = append(system, l)
			continue
		}
		lanes = append(lanes, l)
	}
	WriteJSON(w, 200, ProjectLanes{Lanes: lanes, System: system})
}

// LaneView is an available workflow and its usage in this project.
type LaneView struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Tier        string           `json:"tier"`
	Origin      string           `json:"origin"`
	Version     int              `json:"version"`
	Deprecated  bool             `json:"deprecated,omitempty"`
	Withdrawn   bool             `json:"withdrawn,omitempty"` // stock the release no longer ships
	RunsCount   int              `json:"runsCount"`
	Summary     *workflowSummary `json:"summary,omitempty"`
}

// ProjectLanes separates runnable workflows from AppTown's system jobs.
type ProjectLanes struct {
	Lanes  []LaneView `json:"lanes"`
	System []LaneView `json:"system"`
}
