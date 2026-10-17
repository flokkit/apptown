package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
)

type WorkflowCatalog struct {
	db   *database.DB
	ways *wow.Service
}

func NewWorkflowCatalog(db *database.DB, ways *wow.Service) WorkflowCatalog {
	return WorkflowCatalog{db: db, ways: ways}
}

// LibraryList: GET /api/v1/workflows/library
func (h WorkflowCatalog) LibraryList(w http.ResponseWriter, r *http.Request) {
	catalog, err := loadLibraryCatalog(r.Context(), h.db)
	if err != nil {
		WriteFault(w, err)
		return
	}

	out := []LibraryWorkflow{}
	for _, l := range catalog.workflows {
		if h.ways.Withdrawn(wow.KindWorkflow, l.Origin, l.Name) {
			continue // the library offers what the release ships; selections keep theirs
		}
		yamlSrc := l.Yaml
		stages := []string{}
		kind, category, tier := "", "", "extra"
		system := false
		if doc, err := schema.ParseWorkflowStored([]byte(yamlSrc)); err == nil {
			stages = doc.Spec.StageNames()
			kind = doc.Spec.Kind
			category = doc.Metadata.Category
			tier = doc.Metadata.EffectiveTier()
			system = doc.Metadata.System
		}
		uses := []LibraryWorkflowUse{}
		// Projects that selected this lane (design/decisions/workflows-as-lanes.md);
		// every project for a system workflow.
		projects, origin := catalog.selected[l.Name], "selected"
		if system {
			projects, origin = catalog.projects, "system"
		}
		for _, slug := range projects {
			uses = append(uses, LibraryWorkflowUse{Project: slug, Origin: fmt.Sprintf("%s@%d", origin, l.LatestVersion)})
		}
		out = append(out, LibraryWorkflow{
			Name: l.Name, Description: l.Description, LatestVersion: l.LatestVersion,
			Origin: l.Origin, UpdatedAt: l.UpdatedAt.Format(time.RFC3339Nano), Stages: stages, Kind: kind, Category: category,
			Tier: tier, System: system,
			UsedBy: uses, YAML: yamlSrc,
		})
	}
	WriteJSON(w, 200, out)
}

// WorkflowSchema: GET /api/v1/workflows/schema — the JSON Schema for a
// workflow as a team saves it, for editors and agents authoring one. Rules a
// schema cannot express are the save's (ValidateWorkflow says them).
func (h WorkflowCatalog) WorkflowSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(schema.WorkflowJSONSchema)
}

// ValidateWorkflow: POST /api/v1/workflows/validate {yaml} — live
// editor validation with a structural preview.
func (h WorkflowCatalog) ValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		YAML string `json:"yaml"`
	}) {
		doc, err := schema.ParseWorkflow([]byte(in.YAML))
		if err != nil {
			WriteJSON(w, 200, WorkflowInvalid{OK: false, Error: err.Error()})
			return
		}
		if sch := doc.Spec.On.Schedule; sch != nil {
			if _, err := schema.ParseCron(sch.Cron); err != nil {
				WriteJSON(w, 200, WorkflowInvalid{OK: false, Error: "on.schedule.cron: " + err.Error()})
				return
			}
		}
		preview := []WorkflowStagePreview{}
		for _, st := range doc.Spec.Stages {
			var steps []string
			for _, s := range append(append([]schema.Step{}, st.OnEnter...), st.Steps...) {
				steps = append(steps, s.Name+" ("+s.Uses+")")
			}
			preview = append(preview, WorkflowStagePreview{st.Name, steps})
		}
		WriteJSON(w, 200, WorkflowPreview{OK: true, Name: doc.Metadata.Name,
			Description: doc.Metadata.Description, Kind: doc.Spec.Kind, Stages: preview})
	})
}

// LibraryWorkflowUse is a project that runs a library workflow, and how.
type LibraryWorkflowUse struct {
	Project string `json:"project"`
	Origin  string `json:"origin"`
}

// WorkflowStagePreview is one stage of a workflow being edited.
type WorkflowStagePreview struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

// LibraryWorkflow is a workflow in the library, with the projects using it.
type LibraryWorkflow struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	LatestVersion int64                `json:"latestVersion"`
	Origin        string               `json:"origin"`
	UpdatedAt     string               `json:"updatedAt"`
	Stages        []string             `json:"stages"`
	Kind          string               `json:"kind"`
	Category      string               `json:"category"`
	Tier          string               `json:"tier"`
	System        bool                 `json:"system"`
	UsedBy        []LibraryWorkflowUse `json:"usedBy"`
	YAML          string               `json:"yaml"`
}

// WorkflowInvalid is why a workflow being edited does not validate.
type WorkflowInvalid struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// WorkflowPreview is the structure of a workflow being edited that validates.
type WorkflowPreview struct {
	OK          bool                   `json:"ok"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Kind        string                 `json:"kind"`
	Stages      []WorkflowStagePreview `json:"stages"`
}
