package httpapi

import (
	"fmt"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
)

type ProjectDeletion struct {
	projects *project.Projects
	access   *identity.Service
	deletion *projectlifecycle.Deletion
}

func NewProjectDeletion(projects *project.Projects, access *identity.Service, deletion *projectlifecycle.Deletion) ProjectDeletion {
	return ProjectDeletion{projects: projects, access: access, deletion: deletion}
}

// DeleteProject: DELETE /api/v1/projects/{slug} {confirm} — confirm must
// equal the slug. The owner checks project:delete, refuses while a chat is
// opening or active or the project has running work, and marks it
// deleting under a binding the decision grants: 202 with where the
// deletion stands. The deletion pass then takes its deployments down, main
// last; it is deleted and kept, then purged (design/subsystems/project-deletion.md).
// A project already being deleted is re-confirmed.
func (h ProjectDeletion) DeleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: fmt.Sprintf("confirm must equal the project slug %q", p.Slug)}, func(in struct {
		Confirm string `json:"confirm"`
	}) {
		if in.Confirm != p.Slug {
			WriteError(w, 400, "confirm must equal the project slug %q", p.Slug)
			return
		}
		h.answer(w, http.StatusAccepted)(h.deletion.Request(r.Context(), p.ID))
	})
}

// ProjectDeletionStatus: GET /api/v1/projects/{slug}/deletion — where the
// project's deletion stands: deleting (what it waits on), deleted (kept
// until), or active.
func (h ProjectDeletion) ProjectDeletionStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	h.answer(w, http.StatusOK)(h.deletion.Status(r.Context(), p.ID))
}

// CancelProjectDeletion: POST /api/v1/projects/{slug}/deletion/cancel —
// before the first take-down only.
func (h ProjectDeletion) CancelProjectDeletion(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	h.answer(w, http.StatusOK)(h.deletion.Cancel(r.Context(), p.ID))
}

// RevokeProjectDeletion: POST /api/v1/projects/{slug}/deletion/revoke
// {why} — withdraw the deletion's authority: the pass and the purge stop.
func (h ProjectDeletion) RevokeProjectDeletion(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Why string `json:"why"`
	}) {
		h.answer(w, http.StatusOK)(h.deletion.RevokeBinding(r.Context(), p.ID, in.Why))
	})
}

// RestoreProject: POST /api/v1/projects/{slug}/restore — a deleted project
// back to active, with no deployments, until its purge was admitted.
func (h ProjectDeletion) RestoreProject(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	h.answer(w, http.StatusOK)(h.deletion.Restore(r.Context(), p.ID))
}

// PurgeProject: POST /api/v1/projects/{slug}/purge — an instance
// administrator purges a deleted project now: 202 with the operation.
func (h ProjectDeletion) PurgeProject(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	op, err := h.deletion.PurgeNow(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteAdmitted(w, op)
}

func (h ProjectDeletion) answer(w http.ResponseWriter, status int) func(projectlifecycle.Status, error) {
	return func(out projectlifecycle.Status, err error) {
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, status, ProjectDeletionState(out))
	}
}

// ProjectDeletionState is where a project's deletion stands.
type ProjectDeletionState = projectlifecycle.Status

// ProjectCleanups: GET /api/v1/project-cleanups — admin. Recent deleted
// projects' cleanups: pending, done, or waiting for a person.
func (h ProjectDeletion) ProjectCleanups(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	list, err := h.deletion.Cleanups(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ProjectCleanupList{Cleanups: list})
}

// RetryProjectCleanup: POST /api/v1/project-cleanups/{id}/retry — admin.
// Starts a new round of a cleanup that needs attention.
func (h ProjectDeletion) RetryProjectCleanup(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.deletion.RetryCleanup(r.Context(), id, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ProjectCleanupList is recent deleted projects' cleanups, newest first.
type ProjectCleanupList struct {
	Cleanups []project.Deletion `json:"cleanups"`
}
