package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

// WhatIfs are a pack version's what-ifs (design/subsystems/conformance.md):
// hypothetical, never standing. Starting one takes the authority of a
// fleet review; reading one takes conformance read, as reviews do.
type WhatIfs struct {
	projects    *project.Projects
	conformance *conformance.Service
	runner      whatIfRunner
	library     whatIfPackLibrary
}

// whatIfPackLibrary is the pack library's names and each pack's newest version,
// for offering "each project's own pack at its newest" with a what-if.
type whatIfPackLibrary interface {
	Packs(ctx context.Context) ([]wow.Pack, error)
	PackHistory(ctx context.Context, name string) (*wow.PackHistory, error)
}

// whatIfRunner starts a what-if and returns its id, and says who one runs
// on when it names no projects.
type whatIfRunner interface {
	StartWhatIf(ctx context.Context, in conformance.WhatIfRequest) (ids.ID, error)
	EligibleForWhatIf(ctx context.Context, pack string, version int64, also ...conformance.PackVersion) ([]string, error)
}

func NewWhatIfs(projects *project.Projects, conformance *conformance.Service, runner whatIfRunner, library whatIfPackLibrary) WhatIfs {
	return WhatIfs{projects: projects, conformance: conformance, runner: runner, library: library}
}

// StartWhatIf: POST /api/v1/conformance/what-if {pack, version,
// projects?:[slug…], rubrics, spendLimitMicros} — judge the version's checks
// on the projects' main beside what they adopted, for projects on the pack
// or not; without projects, on every eligible one (on an earlier version of
// the pack). Model rubrics only with rubrics and a spend limit. It runs in
// the background; read it by its id.
func (h WhatIfs) StartWhatIf(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		Pack             string                    `json:"pack"`
		Version          int64                     `json:"version"`
		Projects         []string                  `json:"projects"`
		Rubrics          bool                      `json:"rubrics"`
		SpendLimitMicros int64                     `json:"spendLimitMicros"`
		AdvanceWorkflows bool                      `json:"advanceWorkflows"`
		Also             []conformance.PackVersion `json:"also"`
	}) {
		req := conformance.WhatIfRequest{Pack: strings.TrimSpace(in.Pack), Version: in.Version, Rubrics: in.Rubrics, SpendLimitMicros: in.SpendLimitMicros, AdvanceWorkflows: in.AdvanceWorkflows, Also: in.Also}
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
	id, err := pathID(r, "id")
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

// RecentWhatIfs: GET /api/v1/conformance/what-ifs — the most recent
// what-ifs, newest first, with each project's answer.
func (h WhatIfs) RecentWhatIfs(w http.ResponseWriter, r *http.Request) {
	out, err := h.conformance.RecentWhatIfs(r.Context(), recentWhatIfs)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RecentWhatIfs{WhatIfs: out})
}

// recentWhatIfs is how many the list shows.
const recentWhatIfs = 20

// RecentWhatIfs is the list under Conformance.
type RecentWhatIfs struct {
	WhatIfs []conformance.WhatIf `json:"whatIfs"`
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
	also, err := parseAlso(r.URL.Query().Get("also"))
	if err != nil {
		WriteError(w, 400, "%v", err)
		return
	}
	if err := (conformance.WhatIfRequest{Pack: pack, Version: version, Also: also}).CheckAlso(); err != nil {
		WriteFault(w, err)
		return
	}
	eligible, err := h.runner.EligibleForWhatIf(r.Context(), pack, version, also...)
	if err != nil {
		WriteFault(w, err)
		return
	}
	others, err := h.ownPacksNewest(r, pack)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, LatestWhatIf{WhatIf: out, Eligible: eligible, OwnPacksNewest: others})
}

// ownPacksNewest is every other offered pack, at its newest version, that
// some project the caller may read holds below it: the explicit versions
// "also move each project's own pack to its newest" sends. It is read
// before eligibility, never from it, so a project current on the named pack
// but behind on its own is found.
func (h WhatIfs) ownPacksNewest(r *http.Request, named string) ([]conformance.PackVersion, error) {
	out := []conformance.PackVersion{}
	if h.library == nil {
		return out, nil
	}
	packs, err := h.library.Packs(r.Context())
	if err != nil {
		return nil, err
	}
	for _, p := range packs {
		if p.Name == named || p.Platform {
			continue
		}
		hist, err := h.library.PackHistory(r.Context(), p.Name)
		if err != nil {
			return nil, err
		}
		if hist.Latest < 1 {
			continue
		}
		behind, err := h.runner.EligibleForWhatIf(r.Context(), p.Name, hist.Latest)
		if err != nil {
			return nil, err
		}
		if len(behind) > 0 {
			out = append(out, conformance.PackVersion{Pack: p.Name, Version: hist.Latest})
		}
	}
	return out, nil
}

// parseAlso reads "pack@version,pack@version".
func parseAlso(s string) ([]conformance.PackVersion, error) {
	var out []conformance.PackVersion
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		name, v, ok := strings.Cut(part, "@")
		n, err := strconv.ParseInt(v, 10, 64)
		if !ok || err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("other packs are pack@version, comma-separated: %q", part)
		}
		out = append(out, conformance.PackVersion{Pack: strings.TrimSpace(name), Version: n})
	}
	return out, nil
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
	// OwnPacksNewest is every other pack some project holds below its
	// newest version, at that version: what "also move each project's own
	// pack" sends as also.
	OwnPacksNewest []conformance.PackVersion `json:"ownPacksNewest"`
}
