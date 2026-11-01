package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type Traces struct {
	work *workflow.Service
	ways *wow.Service
	db   *database.DB
}

func NewTraces(work *workflow.Service, ways *wow.Service, db *database.DB) Traces {
	return Traces{work: work, ways: ways, db: db}
}
func (h Traces) Trace(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	snapshot, err := h.work.Snapshot(r.Context(), id, workflow.TraceView)
	if err != nil {
		WriteFault(w, err)
		return
	}
	run := snapshot.Run
	projectSlugMeta, projectSlugErr := project.ByIDIn(r.Context(), h.db, run.ProjectID)
	projectSlug := projectSlugMeta.Slug
	if _, err := database.Lookup(projectSlugErr); err != nil {
		WriteFault(w, err)
		return
	}

	var item *workflow.Change
	if !run.ChangeID.IsZero() {
		item, _ = workflow.ReadChangeIn(r.Context(), h.db, run.ChangeID)
	}

	// The run's immutable definition is the authoritative execution receipt.
	// Library YAML is supplementary; absence never selects a retired project copy.
	var workflowYAML string
	if run.WorkflowVersion > 0 {
		_, y, err := h.ways.WorkflowVersion(r.Context(), run.WorkflowName, run.WorkflowVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			WriteFault(w, err)
			return
		}
		workflowYAML = y
	}

	WriteJSON(w, 200, RunTrace{
		Run:         run,
		ProjectSlug: projectSlug,
		Change:      item,
		Workflow: TraceWorkflow{
			Name: run.WorkflowName, Version: run.WorkflowVersion,
			YAML: workflowYAML, Definition: snapshot.Definition,
		},
		Stages:    snapshot.Stages,
		Steps:     snapshot.Steps,
		Events:    snapshot.Events,
		Artifacts: snapshot.Artifacts,
		Guidance:  snapshot.PinnedGuidance(),
	})
}

// RunTrace is a run's full record: the workflow it ran under, every stage,
// step, event and artifact, and the guidance pinned for it.
type RunTrace struct {
	Run         *workflow.Run       `json:"run"`
	ProjectSlug string              `json:"projectSlug"`
	Change      *workflow.Change    `json:"change"`
	Workflow    TraceWorkflow       `json:"workflow"`
	Stages      []string            `json:"stages"`
	Steps       []*workflow.Step    `json:"steps"`
	Events      []workflow.Event    `json:"events"`
	Artifacts   []workflow.Artifact `json:"artifacts"`
	Guidance    json.RawMessage     `json:"guidance"`
}

// TraceWorkflow is the definition a run executed, and its library YAML.
type TraceWorkflow struct {
	Name       string       `json:"name"`
	Version    int          `json:"version"`
	YAML       string       `json:"yaml"`
	Definition *schema.Spec `json:"definition"`
}
