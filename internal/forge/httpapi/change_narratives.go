package httpapi

import (
	"context"
	"log"
	"net/http"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

type ChangeNarratives struct {
	work       *workflow.Service
	projects   *project.Projects
	access     *identity.Service
	narratives NarrativeRefresher
	authMode   string
}

func NewChangeNarratives(work *workflow.Service, projects *project.Projects, access *identity.Service, narratives NarrativeRefresher, authMode string) ChangeNarratives {
	return ChangeNarratives{work: work, projects: projects, access: access, narratives: narratives, authMode: authMode}
}

// DistillChange: POST /api/v1/changes/{id}/distill — re-distill the
// living narrative now (the automatic triggers are run completion and
// console session end; this is the cockpit's refresh).
func (h ChangeNarratives) DistillChange(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.narratives.Refresh(r.Context(), id); err != nil {
		// Work classifies the Change it cannot find; the id is the request's
		// word. A missing row the owner did not classify stays a failure.
		if fault.KindOf(err) == fault.NotFound {
			WriteError(w, 404, "no Change has id %d", id)
			return
		}
		WriteFault(w, err)
		return
	}
	ch, err := h.work.Change(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, NarrativeRefreshed{OK: true, Summary: ch.Summary})
}

// ImportBranch: POST /api/v1/projects/{slug}/import {branch, title} —
// adopt an outside branch as a Change with reconstructed provenance.
func (h ChangeNarratives) ImportBranch(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "branch required"}, func(in struct{ Branch, Title string }) {
		if in.Branch == "" {
			WriteError(w, 400, "branch required")
			return
		}

		ch, err := h.work.Import(r.Context(), workflow.ImportBranch{ProjectID: p.ID, Branch: in.Branch, Title: in.Title})
		if err != nil {
			WriteFaultOr(w, err, 422)
			return
		}
		if err := h.narratives.Request(r.Context(), ch.ID); err != nil {
			log.Printf("change %d: asking for its narrative: %v", ch.ID, err)
		}
		WriteJSON(w, 200, ChangeEnvelope{Change: *ch})
	})
}

// NarrativeRefresher refreshes a Change's narrative now, or queues a
// refresh (Request).
type NarrativeRefresher interface {
	Request(context.Context, ids.ID) error
	Refresh(context.Context, ids.ID) error
}

// NarrativeRefreshed carries a Change's freshly distilled summary.
type NarrativeRefreshed struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
}
