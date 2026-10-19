package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"
)

type RemediationList struct {
	Remediations []remediation.Remediation `json:"remediations"`
	Occupancy    remediation.Occupancy     `json:"occupancy"`
}
type RemediationDetail struct {
	Remediation remediation.Remediation          `json:"remediation"`
	Items       []remediation.Item               `json:"items"`
	Attempts    map[ids.ID][]remediation.Attempt `json:"attempts"`
	Occupancy   remediation.Occupancy            `json:"occupancy"`
}
type Remediations struct{ fixes *remediation.Service }

func NewRemediations(fixes *remediation.Service) Remediations { return Remediations{fixes: fixes} }

// ListRemediations: GET /api/v1/conformance/remediations
func (h Remediations) ListRemediations(w http.ResponseWriter, r *http.Request) {
	rems, err := h.fixes.Remediations(r.Context(), 50)
	if err != nil {
		WriteFault(w, err)
		return
	}
	occupancy, err := h.fixes.Occupancy(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RemediationList{Remediations: rems, Occupancy: occupancy})
}

// StartRemediation: POST /api/v1/conformance/remediations
// {scope: all|check|project|review, ref, concurrency?}
func (h Remediations) StartRemediation(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Scope       string `json:"scope"`
		Ref         string `json:"ref"`
		Concurrency int    `json:"concurrency"`
		Preview     bool   `json:"preview"` // just say what it would cover
	}) {
		if in.Scope == "" {
			in.Scope = "all"
		}
		if in.Preview {
			findings, err := h.fixes.Scope(r.Context(), in.Scope, strings.TrimSpace(in.Ref))
			if err != nil {
				WriteError(w, 422, "%v", err)
				return
			}
			projects := map[string]bool{}
			for _, f := range findings {
				projects[f.ProjectSlug] = true
			}
			WriteJSON(w, 200, RemediationPreview{Items: len(findings), Projects: len(projects), Findings: findings})
			return
		}
		m, items, err := h.fixes.Start(r.Context(), in.Scope, strings.TrimSpace(in.Ref), Actor(r), in.Concurrency)
		if err != nil {
			// Unclassified failures keep the 422 this route has always answered.
			WriteFaultOr(w, err, http.StatusUnprocessableEntity)
			return
		}
		h.fixes.Wake(r.Context()) // do not make the person wait for the next tick
		WriteJSON(w, 202, RemediationStarted{Remediation: *m, Items: items})
	})
}

// Remediation: GET /api/v1/conformance/remediations/{id}
func (h Remediations) Remediation(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	m, err := h.fixes.Remediation(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	items, err := h.fixes.Items(r.Context(), &id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	attempts, err := h.fixes.Attempts(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	occupancy, err := h.fixes.Occupancy(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RemediationDetail{Remediation: *m, Items: items, Attempts: attempts, Occupancy: occupancy})
}

// RemediationAction: POST /api/v1/conformance/remediations/{id}/{action}
func (h Remediations) RemediationAction(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	var aerr error
	switch r.PathValue("action") {
	case "pause":
		aerr = h.fixes.Pause(r.Context(), id, Actor(r))
	case "resume":
		aerr = h.fixes.Resume(r.Context(), id, Actor(r))
	case "cancel":
		aerr = h.fixes.Cancel(r.Context(), id, Actor(r))
	default:
		WriteError(w, 404, "unknown action")
		return
	}
	if aerr != nil {
		WriteError(w, 422, "%v", aerr)
		return
	}
	h.fixes.Wake(r.Context())
	WriteJSON(w, 200, Done{OK: true})
}

// RemediationItemAction: POST /api/v1/conformance/remediations/{id}/items/{item}/{action}
func (h Remediations) RemediationItemAction(w http.ResponseWriter, r *http.Request) {
	jobID, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad remediation id")
		return
	}

	itemID, err := PathID(r, "item")
	if err != nil {
		WriteError(w, 400, "bad item id")
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Note   string `json:"note"`
		Reason string `json:"reason"`
	}) {
		var aerr error
		switch r.PathValue("action") {
		case "retry":
			aerr = h.fixes.RetryItem(r.Context(), jobID, itemID, Actor(r), in.Note)
		case "skip":
			aerr = h.fixes.SkipItem(r.Context(), jobID, itemID, Actor(r), strs.FirstNonBlank(in.Reason, in.Note))
		default:
			WriteError(w, 404, "unknown action")
			return
		}
		if aerr != nil {
			WriteFault(w, aerr)
			return
		}
		h.fixes.Wake(r.Context())
		WriteJSON(w, 200, Done{OK: true})
	})
}

// Workers: GET/PUT /api/v1/workers — the declared pools by kind.
func (h Remediations) Workers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		jsonCommand(w, r, jsonBody{}, func(in struct {
			Kind  string `json:"kind"`
			Count int    `json:"count"`
		}) {
			if in.Kind == "" {
				in.Kind = remediation.WorkerKindRemediation
			}
			if in.Kind != remediation.WorkerKindRemediation {
				WriteError(w, 422, "unknown worker kind %q (only %s exists yet)", in.Kind, remediation.WorkerKindRemediation)
				return
			}
			if err := h.fixes.SetWorkerCount(r.Context(), in.Kind, in.Count, Actor(r)); err != nil {
				WriteError(w, 422, "%v", err)
				return
			}
			h.fixes.Wake(r.Context())
			h.writeWorkers(r.Context(), w)
		})
		return
	}
	h.writeWorkers(r.Context(), w)
}
func (h Remediations) writeWorkers(ctx context.Context, w http.ResponseWriter) {
	WriteJSON(w, 200, WorkerPools{Pools: []WorkerPool{{
		Kind: remediation.WorkerKindRemediation, Count: h.fixes.WorkerCount(ctx, remediation.WorkerKindRemediation),
		Gloss: fmt.Sprintf("how many fixes AppTown works on at once (each one a Change on its own project); the pool holds off for %s after a provider rate limit", remediation.Cooldown),
	}}})
}

// RemediationPreview says what a remediation would cover, without starting it.
type RemediationPreview struct {
	Items    int                       `json:"items"`
	Projects int                       `json:"projects"`
	Findings []conformance.Observation `json:"findings"`
}

// RemediationStarted is the remediation just started and its items.
type RemediationStarted struct {
	Remediation remediation.Remediation `json:"remediation"`
	Items       []remediation.Item      `json:"items"`
}

// WorkerPools is the declared worker pools by kind.
type WorkerPools struct {
	Pools []WorkerPool `json:"pools"`
}

// WorkerPool is how many workers of one kind run at once, and what that means.
type WorkerPool struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Gloss string `json:"gloss"`
}
