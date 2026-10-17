package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
)

// WhatIfs are a pack version's what-ifs (design/subsystems/conformance.md):
// hypothetical, never standing. Starting one takes the authority of a
// fleet review; reading one takes conformance read, as reviews do.
type WhatIfs struct {
	projects    *project.Projects
	conformance *conformance.Service
	runner      WhatIfRunner
}

// WhatIfRunner starts a what-if and returns its id, and says who one runs
// on when it names no projects.
type WhatIfRunner interface {
	StartWhatIf(ctx context.Context, in conformance.WhatIfRequest) (ids.ID, error)
	EligibleForWhatIf(ctx context.Context, pack string, version int64) ([]string, error)
}

func NewWhatIfs(projects *project.Projects, conformance *conformance.Service, runner WhatIfRunner) WhatIfs {
	return WhatIfs{projects: projects, conformance: conformance, runner: runner}
}

// StartWhatIf: POST /api/v1/conformance/what-if {pack, version,
// projects?:[slug…], rubrics, spendLimitMicros} — judge the version's checks
// on the projects' main beside what they adopted, for projects on the pack
// or not; without projects, on every eligible one (on an earlier version of
// the pack). Model rubrics only with rubrics and a spend limit. It runs in
// the background; read it by its id.
func (h WhatIfs) StartWhatIf(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		Pack             string   `json:"pack"`
		Version          int64    `json:"version"`
		Projects         []string `json:"projects"`
		Rubrics          bool     `json:"rubrics"`
		SpendLimitMicros int64    `json:"spendLimitMicros"`
	}) {
		req := conformance.WhatIfRequest{Pack: strings.TrimSpace(in.Pack), Version: in.Version, Rubrics: in.Rubrics, SpendLimitMicros: in.SpendLimitMicros}
		if in.Projects != nil && len(in.Projects) == 0 {
			WriteError(w, 400, "name the projects to run the what-if on, or leave projects out for every eligible one")
			return
		}
		for _, slug := range in.Projects {
			p, err := h.projects.BySlug(r.Context(), strings.TrimSpace(slug))
			if err != nil {
				writeReadFault(w, err, "no such project %q", slug)
				return
			}
			req.Projects = append(req.Projects, p.ID)
		}
		id, err := h.runner.StartWhatIf(r.Context(), req)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 202, WhatIfStarted{ID: id})
	})
}

// WhatIf: GET /api/v1/conformance/what-if/{id}.
func (h WhatIfs) WhatIf(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad what-if id")
		return
	}
	out, err := h.conformance.WhatIf(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// LatestWhatIf: GET /api/v1/conformance/what-if?pack=P&version=N — the
// newest what-if of exactly that version ({"whatIf": null} when none ran)
// and the projects one would run on by default, those the caller may read.
func (h WhatIfs) LatestWhatIf(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	pack := strings.TrimSpace(r.URL.Query().Get("pack"))
	if err != nil || version < 1 || pack == "" {
		WriteError(w, 400, "name a pack and a version")
		return
	}
	out, err := h.conformance.LatestWhatIf(r.Context(), pack, version)
	if err != nil {
		WriteFault(w, err)
		return
	}
	eligible, err := h.runner.EligibleForWhatIf(r.Context(), pack, version)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, LatestWhatIf{WhatIf: out, Eligible: eligible})
}

// WhatIfStarted is the id of a what-if that is running.
type WhatIfStarted struct {
	ID ids.ID `json:"id"`
}

// LatestWhatIf is a version's newest what-if, or none, and the projects a
// what-if runs on when none are named.
type LatestWhatIf struct {
	WhatIf   *conformance.WhatIf `json:"whatIf"`
	Eligible []string            `json:"eligible"`
}
