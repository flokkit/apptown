package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/project"
)

type ADRs struct {
	adrs     *adrs.Service
	projects *project.Projects
}

func NewADRs(adrs *adrs.Service, projects *project.Projects) ADRs {
	return ADRs{adrs: adrs, projects: projects}
}
func (h ADRs) ADRLibrary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := adrs.Filter{ProjectSlug: query.Get("project"), Tag: query.Get("tag"), Query: query.Get("q")}
	if text := query.Get("status"); text != "" {
		state, err := adrs.ParseState(text)
		if err != nil {
			WriteFault(w, err)
			return
		}
		filter.State = state
	}
	out, err := h.adrs.List(r.Context(), filter)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

func (h ADRs) ListProjectADRs(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.adrs.List(r.Context(), adrs.Filter{ProjectID: p.ID})
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

func (h ADRs) CreateADR(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorFormat: "bad JSON: %v"}, func(in struct {
		Title    string `json:"title"`
		BodyMD   string `json:"bodyMd"`
		Tags     string `json:"tags"`
		ChangeID ids.ID `json:"changeId"`
	}) {
		out, err := h.adrs.Create(r.Context(), adrs.Create{ProjectID: p.ID, Title: in.Title, BodyMD: in.BodyMD, Tags: in.Tags, ChangeID: in.ChangeID})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, out)
	})
}

func (h ADRs) GetADR(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	out, err := h.adrs.Get(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

func (h ADRs) PatchADR(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{errorFormat: "bad JSON: %v"}, func(in struct {
		Title    *string `json:"title"`
		BodyMD   *string `json:"bodyMd"`
		Tags     *string `json:"tags"`
		Status   *string `json:"status"`
		ChangeID *ids.ID `json:"changeId"`
		// Expected is the text the edit started from, for each text field
		// it changes; a field edited since is refused (409, adr-text-moved).
		Expected struct {
			Title  *string `json:"title"`
			BodyMD *string `json:"bodyMd"`
			Tags   *string `json:"tags"`
		} `json:"expected"`
	}) {
		command := adrs.Edit{ID: id, Title: in.Title, BodyMD: in.BodyMD, Tags: in.Tags, ChangeID: in.ChangeID,
			Expected: adrs.TextFields{Title: in.Expected.Title, BodyMD: in.Expected.BodyMD, Tags: in.Expected.Tags}}
		if in.Status != nil {
			command.Decision, err = adrs.ParseDecision(*in.Status)
			if err != nil {
				WriteFault(w, err)
				return
			}
		}
		out, err := h.adrs.Edit(r.Context(), command)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}

func (h ADRs) SupersedeADR(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{optional: true, errorFormat: "bad JSON: %v"}, func(in struct {
		Title  string `json:"title"`
		BodyMD string `json:"bodyMd"`
	}) {
		out, err := h.adrs.ProposeSuccessor(r.Context(), adrs.Successor{ID: id, Title: in.Title, BodyMD: in.BodyMD})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, out)
	})
}
