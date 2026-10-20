package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/workflow"
)

type RemediationChanges struct {
	projects  *project.Projects
	access    *identity.Service
	checks    *conformance.Service
	work      *workflow.Service
	lifecycle *projectlifecycle.Service
	db        *database.DB
	authMode  string
	now       func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewRemediationChanges(projects *project.Projects, access *identity.Service, checks *conformance.Service, work *workflow.Service, lifecycle *projectlifecycle.Service, db *database.DB, authMode string, now func() time.Time) RemediationChanges {
	return RemediationChanges{projects: projects, access: access, checks: checks, work: work, lifecycle: lifecycle, db: db, authMode: authMode, now: now}
}

// OpenRemediation: POST /api/v1/conformance/remediate
// {project, block, version, check, reviewId, detail} — open ONE ordinary Change
// on the project carrying the failing check as its acceptance criterion (M6).
// The Change starts on the project's quick-change lane (else implement) when it has one; otherwise
// it is opened without a run and the caller is told.
func (h RemediationChanges) OpenRemediation(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Project  string `json:"project"`
		Block    string `json:"block"`
		Version  int    `json:"version"`
		Check    string `json:"check"`
		ReviewID ids.ID `json:"reviewId"`
		Detail   string `json:"detail"`
	}) {
		p, err := h.projects.BySlug(r.Context(), strings.TrimSpace(in.Project))
		if err != nil {
			writeReadFault(w, err, "no such project %q", in.Project)
			return
		}
		if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
			return
		}
		sc, err := h.checks.AdoptedCheck(r.Context(), p.ID, in.Block, in.Check)
		if errors.Is(err, conformance.ErrCheckNotAdopted) {
			// The slug is the request's word; the owner knows only the id.
			WriteError(w, 422, "%s no longer adopts %s · %s — nothing to remediate", p.Slug, in.Block, in.Check)
			return
		}
		if err != nil {
			WriteFault(w, err)
			return
		}
		if why := remediation.Unprovable(sc); why != "" {
			WriteError(w, 422, "%s", why)
			return
		}
		body := remediation.Brief(sc.Block, sc.Version, sc, in.Detail)
		item, err := h.work.Create(r.Context(), workflow.ChangeDraft{ProjectID: p.ID, Type: "remediation", Title: remediation.ChangeTitle(sc), Body: body, Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{remediation.AcceptanceCheck(sc)}}})
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		// A refused lane leaves the one recorded change available for manual work.
		run, err := h.work.StartRun(r.Context(), workflow.StartChangeRun{ChangeID: item.ID, Workflow: remediation.WorkflowFor(r.Context(), h.lifecycle, &p)})
		started := err == nil
		if started {
			item, err = workflow.ReadChangeIn(r.Context(), h.db, item.ID)
			if err != nil {
				WriteFault(w, err)
				return
			}
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "conformance.remediation-opened", fmt.Sprintf("%d", item.ID),
			map[string]any{"review": in.ReviewID, "block": in.Block, "version": in.Version, "check": in.Check, "started": started})
		WriteJSON(w, 201, RemediationOpened{Change: *item, Run: run, Started: started})
	})
}

// RemediationOpened is the change opened to fix one check, and its run when
// the lane started (a refused lane leaves the change for manual work).
type RemediationOpened struct {
	Change  workflow.Change `json:"change"`
	Run     *workflow.Run   `json:"run"`
	Started bool            `json:"started"`
}
