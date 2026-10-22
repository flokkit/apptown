package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type ProjectWorkflows struct {
	projects *project.Projects
	adoption *adoption.Service
	ways     *wow.Service
	work     *workflow.Service
	db       *database.DB
}

func NewProjectWorkflows(projects *project.Projects, adoption *adoption.Service, ways *wow.Service, work *workflow.Service, db *database.DB) ProjectWorkflows {
	return ProjectWorkflows{projects: projects, adoption: adoption, ways: ways, work: work, db: db}
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
	// Withdrawn: stock the running release no longer ships; still selected.
	Withdrawn bool `json:"withdrawn,omitempty"`
}

func (h ProjectWorkflows) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// Library-backed (design/decisions/workflows-as-lanes.md): the project's selected
	// lanes plus every system workflow, at the library's latest version.
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

// ProjectLanes: GET /api/v1/projects/{slug}/lanes — every lane in the
// library (core and extras) with whether this project selected it, and
// the system workflows that apply regardless.
func (h ProjectWorkflows) ProjectLanes(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	usage, err := workflow.RunCountsByWorkflowIn(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	library, err := h.ways.Workflows(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	lanes, system := []LaneView{}, []LaneView{}
	for _, e := range library {
		l := LaneView{Name: e.Name, Description: e.Doc.Metadata.Description, Tier: e.Doc.Metadata.EffectiveTier(),
			Origin: e.Origin, Version: e.Version, Deprecated: e.Doc.Metadata.Deprecated, //lint:ignore SA1019 reading the deprecated flag is the point
			RunsCount: usage[e.Name], Summary: summarizeWorkflow(e.YAML)}
		if e.Doc.Metadata.System {
			l.Selected = true
			system = append(system, l)
			continue
		}
		selected, err := h.adoption.LaneSelected(r.Context(), p.ID, e.Name)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if e.Withdrawn && !selected {
			continue // not offered; a project that selected it keeps it, labelled
		}
		l.Selected, l.Withdrawn = selected, e.Withdrawn
		lanes = append(lanes, l)
	}
	WriteJSON(w, 200, ProjectLanes{Lanes: lanes, System: system})
}

// LaneView is a workflow in the library and whether this project selected it.
type LaneView struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Tier        string           `json:"tier"`
	Origin      string           `json:"origin"`
	Version     int              `json:"version"`
	Selected    bool             `json:"selected"`
	Deprecated  bool             `json:"deprecated,omitempty"`
	Withdrawn   bool             `json:"withdrawn,omitempty"` // stock the release no longer ships
	RunsCount   int              `json:"runsCount"`
	Summary     *workflowSummary `json:"summary,omitempty"`
}

// ProjectLanes is every lane in the library with the project's selection,
// and the system workflows that apply regardless.
type ProjectLanes struct {
	Lanes  []LaneView `json:"lanes"`
	System []LaneView `json:"system"`
}
