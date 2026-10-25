package httpapi

import (
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type ProjectServices struct {
	projects *project.Projects
	access   *identity.Service
	editor   *repositoryedit.Service
	dataDir  string
	authMode string
}

func NewProjectServices(projects *project.Projects, access *identity.Service, editor *repositoryedit.Service, dataDir string, authMode string) ProjectServices {
	return ProjectServices{projects: projects, access: access, editor: editor, dataDir: dataDir, authMode: authMode}
}

// The project services routes' responses.
type (
	// ProjectServiceList is the needs the contract declares, which contract
	// form the project is on, and the declaration an edit starts from.
	ProjectServiceList struct {
		Needs       []DeclaredNeed `json:"needs"`
		Contract    string         `json:"contract"`
		Declaration string         `json:"declaration"`
	}
	// DeclaredNeed is one catalog capability the contract needs.
	DeclaredNeed struct {
		Capability string            `json:"capability"`
		Params     map[string]string `json:"params,omitempty"`
	}
	// ServicesSeeded answers needs written straight into a new project.
	ServicesSeeded struct {
		Needs  []repositoryedit.NeedSelection `json:"needs"`
		Direct bool                           `json:"direct"`
	}
	// ServicesProposed answers needs proposed through a Change.
	ServicesProposed struct {
		ChangeID     ids.ID                         `json:"changeId"`
		ChangeNumber int64                          `json:"changeNumber"` // within the project
		Needs        []repositoryedit.NeedSelection `json:"needs"`
	}
)

// ListProjectServices: GET /projects/{slug}/services — the declared needs,
// which contract form the project is on, and the declaration's version.
func (h ProjectServices) ListProjectServices(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	repo, err := p.Repository(h.dataDir)
	if err != nil {
		WriteFault(w, err)
		return
	}
	// The version is read first: if a landing moves main between the two
	// reads, the edit names the older declaration and its save is refused.
	version, err := repositoryedit.DeclarationVersion(repo)
	if err != nil {
		WriteError(w, 500, "reading the app contract: %v", err)
		return
	}
	form := "none"
	needs := []DeclaredNeed{}
	c, err := docker.ReadWorkspaceContract(repo)
	if err != nil && !errors.Is(err, docker.ErrNoContract) {
		WriteError(w, 422, "%v", err)
		return
	}
	if err == nil {
		form = "app.yaml"
		for _, n := range c.Needs {
			needs = append(needs, DeclaredNeed{Capability: n, Params: c.NeedParams[n]})
		}
	}
	WriteJSON(w, 200, ProjectServiceList{Needs: needs, Contract: form, Declaration: version})
}

// SetProjectServices: POST /projects/{slug}/services {needs, title,
// expectedDeclaration} — open a Change that sets the needs in
// .apptown/app.yaml. {needs, direct: true} seeds a new project's main instead,
// refused once the project has a Change.
func (h ProjectServices) SetProjectServices(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
		return
	}
	if p.DefaultBranch == "" {
		WriteError(w, 422, "project is not repo-backed")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Needs               []repositoryedit.NeedSelection `json:"needs"` // catalog capabilities with params, passed through verbatim
		Title               string                         `json:"title"`
		Direct              bool                           `json:"direct"`
		ExpectedDeclaration *string                        `json:"expectedDeclaration"`
	}) {
		if in.Needs == nil {
			in.Needs = []repositoryedit.NeedSelection{}
		}
		ch, err := h.editor.SetServices(r.Context(), p, repositoryedit.ServicesEdit{Needs: in.Needs, Title: in.Title, Actor: Actor(r), Direct: in.Direct, ExpectedDeclaration: in.ExpectedDeclaration})
		if err != nil {
			WriteFault(w, err)
			return
		}
		if in.Direct {
			WriteJSON(w, 200, ServicesSeeded{Needs: in.Needs, Direct: true})
			return
		}
		WriteJSON(w, 200, ServicesProposed{ChangeID: ch.ID, ChangeNumber: ch.Number, Needs: in.Needs})
	})
}
