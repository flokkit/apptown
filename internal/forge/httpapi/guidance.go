package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/wow"
)

type Guidance struct {
	ways      *wow.Service
	adoption  *adoption.Service
	projects  *project.Projects
	lifecycle *projectlifecycle.Service
	inventory SteeringInventory
}

func NewGuidance(ways *wow.Service, adoption *adoption.Service, projects *project.Projects, lifecycle *projectlifecycle.Service, inventory SteeringInventory) Guidance {
	return Guidance{ways: ways, adoption: adoption, projects: projects, lifecycle: lifecycle, inventory: inventory}
}
func (h Guidance) GetProjectGuidance(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// The list and its revision come from one snapshot: an editor saves the
	// list back with this revision.
	view, err := h.adoption.GuidanceSnapshot(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	text, hash, imports, revision := view.Text, view.Hash, view.Imports, view.Revision
	// The generated sections ride along without bodies: the totals count
	// them, the library's Generated section shows them.
	generated := h.inventory.sections(r.Context(), p.ID)
	always, conditional := steeringOverhead(generated)
	for i := range generated {
		generated[i].BodyMD = ""
	}
	WriteJSON(w, 200, ProjectGuidance{
		Revision:  revision,
		Imports:   imports,
		Generated: generated,
		Effective: EffectiveGuidance{
			Text: text, Hash: hash, Bytes: len(text),
			GeneratedBytes: always, ConditionalBytes: conditional, ContextBytes: len(text) + always,
		},
	})
}

// PutProjectGuidance replaces the project's manifest with the given
// ordered import list. version 0 means "pin to current latest" — the stored
// pin is always concrete.
func (h Guidance) PutProjectGuidance(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		ExpectedRevision *int64 `json:"expectedRevision"`
		Imports          []struct {
			Name     string `json:"name"`
			Version  int    `json:"version"`
			Disabled bool   `json:"disabled"`
		} `json:"imports"`
	}) {
		pins := make([]wow.Pin, 0, len(in.Imports))
		for _, im := range in.Imports {
			pins = append(pins, wow.Pin{Name: im.Name, Version: im.Version, Disabled: im.Disabled})
		}
		resolved, err := h.ways.ResolvePins(r.Context(), pins)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if err := h.lifecycle.ReplaceGuidance(r.Context(), p, in.ExpectedRevision, resolved, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		h.GetProjectGuidance(w, r)
	})
}

// GeneratedGuidance: GET /api/v1/guidance/generated — the machine-
// written steering sections, rendered live so the library shows EVERYTHING
// that steers agents: authored blocks (versioned, hash-pinned) and generated
// inventories (instance state; attested per step via steering artifacts).
//
// ?project=<slug> adds that project's own generated sections (integrations).
// Without a project it is the platform's sections, which every project's
// agent steps carry and every project reader already sees in its effective
// guidance. A named project adds its own section and needs read access.
func (h Guidance) GeneratedGuidance(w http.ResponseWriter, r *http.Request) {
	var projectID ids.ID
	if slug := r.URL.Query().Get("project"); slug != "" {
		p, err := h.projects.Readable(r.Context(), slug)
		if err != nil {
			WriteFault(w, err)
			return
		}
		projectID = p.ID
	}
	out := h.inventory.sections(r.Context(), projectID)
	if out == nil {
		out = []steeringSection{}
	}
	always, conditional := steeringOverhead(out)
	WriteJSON(w, 200, GeneratedGuidance{Generated: out, AlwaysBytes: always, ConditionalBytes: conditional})
}

// ProjectGuidance is a project's guidance imports, the generated sections
// that ride along, and what an agent step receives.
type ProjectGuidance struct {
	// Revision is the project's adoption revision, which an edit sends back.
	Revision  int64             `json:"revision"`
	Imports   []adoption.Import `json:"imports"`
	Generated []steeringSection `json:"generated"`
	Effective EffectiveGuidance `json:"effective"`
}

// EffectiveGuidance is the compiled steering and its size. GeneratedBytes
// ride in every agent step; ConditionalBytes are the most a step could add on
// top; ContextBytes are the blocks plus what always rides.
type EffectiveGuidance struct {
	Text             string `json:"text"`
	Hash             string `json:"hash"`
	Bytes            int    `json:"bytes"`
	GeneratedBytes   int    `json:"generatedBytes"`
	ConditionalBytes int    `json:"conditionalBytes"`
	ContextBytes     int    `json:"contextBytes"`
}

// GeneratedGuidance is the machine-written steering sections and their size.
type GeneratedGuidance struct {
	Generated        []steeringSection `json:"generated"`
	AlwaysBytes      int               `json:"alwaysBytes"`
	ConditionalBytes int               `json:"conditionalBytes"`
}
