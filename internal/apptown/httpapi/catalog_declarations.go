package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

type CatalogDeclarations struct {
	ways   *wow.Service
	access *identity.Service
}

func NewCatalogDeclarations(ways *wow.Service, access *identity.Service) CatalogDeclarations {
	return CatalogDeclarations{ways: ways, access: access}
}

// CatalogCreate: POST /catalog {name, description, definition} — admin;
// new entries are drafts.
func (h CatalogDeclarations) CatalogCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsCatalog, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Definition  wow.CatalogDef `json:"definition"`
	}) {
		if err := in.Definition.Validate(); err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		c, err := h.ways.CreateCatalogChange(r.Context(), in.Name, in.Description, in.Definition, Actor(r))
		if err != nil {
			// Unclassified failures keep the 409 this route has always answered.
			WriteFaultOr(w, err, http.StatusConflict)
			return
		}
		WriteJSON(w, 201, c)
	})
}

// CatalogGet: GET /catalog/{name...}
func (h CatalogDeclarations) CatalogGet(w http.ResponseWriter, r *http.Request) {
	c, err := h.ways.CatalogChange(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "%v", err)
		return
	}
	version, ok := QueryInt(w, r, "version", c.LatestVersion)
	if !ok {
		return
	}
	def, err := h.ways.CatalogDefinition(r.Context(), c.ID, version)
	if err != nil {
		WriteError(w, 404, "no such version %d", version)
		return
	}
	WriteJSON(w, 200, CatalogChangeDetail{Change: *c, Status: c.State().String(), ShownVersion: version, Definition: *def})
}

// CatalogPatch: PATCH /catalog/{name...} {definition, description?} —
// admin; a definition change cuts a new version, which is a DRAFT until
// re-promoted (its receipts were earned on the old definition).
func (h CatalogDeclarations) CatalogPatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsCatalog, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	c, err := h.ways.CatalogChange(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "%v", err)
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Description *string         `json:"description"`
		Definition  *wow.CatalogDef `json:"definition"`
	}) {
		if in.Definition != nil {
			if err := in.Definition.Validate(); err != nil {
				WriteError(w, 400, "%v", err)
				return
			}
		}
		out, err := h.ways.EditCatalog(r.Context(), c.ID, wow.CatalogPatch{Description: in.Description, Definition: in.Definition}, Actor(r))
		if err != nil {
			WriteError(w, 500, "saving the edit: %v", err)
			return
		}

		WriteJSON(w, 200, CatalogChangeStatus{Change: *out, Status: out.State().String()})
	})
}

// CatalogPromote: POST /catalog/{name...}/promote {note} — admin.
// Promotion is the claim "this has been run and watched": the note should
// name the evaluation run(s). Promotes the LATEST version.
func (h CatalogDeclarations) CatalogPromote(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsCatalog, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	c, err := h.ways.CatalogChange(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "%v", err)
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Note string `json:"note"`
	}) {
		c, err = h.ways.PromoteCatalogChange(r.Context(), c.Name, in.Note, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, CatalogChangeStatus{Change: *c, Status: c.State().String()})
	})
}

// CatalogChangeDetail is one catalog change at a shown version.
type CatalogChangeDetail struct {
	Change       wow.CatalogChange `json:"change"`
	Status       string            `json:"status"`
	ShownVersion int               `json:"shownVersion"`
	Definition   wow.CatalogDef    `json:"definition"`
}

// CatalogChangeStatus is a catalog change after an edit or promotion.
type CatalogChangeStatus struct {
	Change wow.CatalogChange `json:"change"`
	Status string            `json:"status"`
}
