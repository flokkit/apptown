package httpapi

import (
	"errors"
	"net/http"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

type Retention struct {
	deployment *deployment.Service
	projects   *project.Projects
}

func NewRetention(deployment *deployment.Service, projects *project.Projects) Retention {
	return Retention{deployment: deployment, projects: projects}
}

// project's torn-down deployments left behind.
func (h Retention) ProjectRetained(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	list, err := h.deployment.Retained.ForProject(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RetainedList{Retained: list})
}

// RetainedList is resources torn-down deployments left behind.
type RetainedList struct {
	Retained []deployment.RetainedResource `json:"retained"`
}

// Retained: GET /retained — every retained resource on the instance,
// open ones first for the people who run the targets.
func (h Retention) Retained(w http.ResponseWriter, r *http.Request) {
	list, err := h.deployment.Retained.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RetainedList{Retained: list})
}

// SettleRetained: POST /projects/{slug}/retained/{id} {disposition,
// note} — a person says what became of it: released (deleted at the
// provider) or kept (stays on purpose). The forge records the word.
func (h Retention) SettleRetained(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad retained resource id")
		return
	}
	jsonCommand(w, r, jsonBody{errorFormat: "%v"}, func(in struct {
		Disposition string `json:"disposition"`
		Note        string `json:"note"`
	}) {
		// Unknown wire values become the invalid zero state; the checked command
		// authorizes first and owns the legal-transition rule.
		to, _ := deployment.ParseRetentionState(in.Disposition)
		ctx := r.Context()
		if err := h.deployment.SettleRetained(ctx, deployment.SettleRetained{ProjectID: p.ID, ResourceID: id, Disposition: to, Note: in.Note}); err != nil {
			// These two name the resource and project, which only the request knows.
			if errors.Is(err, deployment.ErrRetentionState) {
				WriteError(w, 422, "disposition must be released (you deleted it at the provider) or kept (it stays on purpose)")
				return
			}
			if errors.Is(err, deployment.ErrRetainedMissing) {
				WriteError(w, 404, "no open retained resource %d on %s", id, p.Slug)
				return
			}
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}
