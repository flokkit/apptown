package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
)

type RepositorySetup struct {
	projects  *project.Projects
	lifecycle *projectlifecycle.Service
	access    *identity.Service
}

func NewRepositorySetup(projects *project.Projects, lifecycle *projectlifecycle.Service, access *identity.Service) RepositorySetup {
	return RepositorySetup{projects: projects, lifecycle: lifecycle, access: access}
}

// Initialize: POST /projects/{slug}/repo {defaultBranch} — init or adopt.
func (h RepositorySetup) Initialize(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in RepositoryStart) {
		if p.DefaultBranch != "" {
			WriteError(w, 409, "project is already repo-backed (branch %q)", p.DefaultBranch)
			return
		}
		if err := h.lifecycle.InitializeRepository(r.Context(), p, in.DefaultBranch, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, p)
	})
}

// SeedFromURL: POST /projects/{slug}/repo/from-url {url, branch?,
// credentialSecret?} — start a new, empty project's repository from an
// https repository: its default branch and tags. Authorized as
// project:import on the project for the caller's own credential. Naming a
// forge secret as the git credential is for administrators only: its
// value goes to the server the caller chose.
func (h RepositorySetup) SeedFromURL(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in RepositoryFromURL) {
		if in.CredentialSecret != "" && !RequireAdmin(h.access, w, r) {
			return
		}
		if err := h.lifecycle.SeedFromURL(r.Context(), p, projectlifecycle.FromURL{URL: in.URL, Branch: in.Branch, CredentialSecret: in.CredentialSecret}, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, p)
	})
}

// SeedFromBundle: POST /projects/{slug}/repo/from-bundle?branch= with a Git
// bundle as the body — start a new, empty project's repository from it:
// its default branch (branch, else the one its HEAD names, else its only
// branch) and tags. Authorized as project:import on the project.
func (h RepositorySetup) SeedFromBundle(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if err := h.lifecycle.SeedFromBundle(r.Context(), p, r.Body, r.URL.Query().Get("branch"), Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, p)
}

// DownloadCode: GET /projects/{slug}/repo/bundle — the project's default
// branch and tags as a Git bundle whose HEAD names the default, for
// anyone who may read the project. A failure after the first byte aborts
// the response, so a broken download never looks complete.
func (h RepositorySetup) DownloadCode(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if p.DefaultBranch == "" {
		WriteError(w, 422, "project %s is not repo-backed", p.Slug)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-bundle")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.Slug+`.bundle"`)
	sw := &startedWriter{w: w}
	if err := h.lifecycle.WriteBundle(r.Context(), p, sw); err != nil {
		if sw.started {
			panic(http.ErrAbortHandler)
		}
		WriteFault(w, err)
	}
}

// startedWriter notes whether anything has been written.
type startedWriter struct {
	w       http.ResponseWriter
	started bool
}

func (s *startedWriter) Write(b []byte) (int, error) {
	s.started = s.started || len(b) > 0
	return s.w.Write(b)
}

// RepositoryStart is POST /projects/{slug}/repo: the default branch of a
// new repository (main when empty).
type RepositoryStart struct {
	DefaultBranch string `json:"defaultBranch"`
}

// RepositoryFromURL is POST /projects/{slug}/repo/from-url: existing code to
// start the project's repository from.
type RepositoryFromURL struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// CredentialSecret names the forge secret offered as the git password.
	CredentialSecret string `json:"credentialSecret"`
}
