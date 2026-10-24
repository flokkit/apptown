package httpapi

import (
	"net/http"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/gitexec"
)

type ProjectManifest struct {
	projects *project.Projects
	adoption *adoption.Service
	db       *database.DB
	dataDir  string
	stores   SecretStores
}

// SecretStores is the secret owner's answer to which stores this AppTown
// can use now, by provider name; a secrets file that cannot be read is an
// error.
type SecretStores interface {
	AvailableProviders() (map[string]bool, error)
}

func NewProjectManifest(projects *project.Projects, adoption *adoption.Service, db *database.DB, dataDir string, stores SecretStores) ProjectManifest {
	return ProjectManifest{projects: projects, adoption: adoption, db: db, dataDir: dataDir, stores: stores}
}

// ProjectManifest: GET /projects/{slug}/manifest — the declarative
// "what this project is wired to": repo, applied packs, workflows with
// origins, pinned guidance, instance bindings. The wizard's landing view and
// the audit answer to "how is this project set up".
func (h ProjectManifest) ProjectManifest(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// repo
	repo := ManifestRepo{Backed: p.DefaultBranch != ""}
	if p.DefaultBranch != "" {
		repo.DefaultBranch = p.DefaultBranch
		ws := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
		if out, err := gitexec.Output(r.Context(), ws, "remote", "get-url", "origin"); err == nil {
			repo.Remote = out
		}
	}
	// packs applied (from the audit trail — application is an audited act)
	packs := []ManifestPack{}
	applied, err := audit.Recent(r.Context(), h.db, audit.Filter{ProjectID: p.ID, KindPrefix: "pack.applied", Limit: audit.MaxLimit})
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := len(applied) - 1; i >= 0; i-- { // the ledger reads newest first; the manifest tells the story in order
		if applied[i].ProjectID == p.ID {
			packs = append(packs, ManifestPack{Name: applied[i].Subject, At: applied[i].At})
		}
	}
	// workflows
	workflows := []ManifestWorkflow{}
	// Every section is required: a manifest missing one would read as a
	// project with nothing adopted there.
	available, err := h.adoption.AvailableWorkflows(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, w := range available {
		workflows = append(workflows, ManifestWorkflow{Name: w.Name, Version: w.Version, Origin: w.OriginRef()})
	}
	// guidance pins
	guidance := []ManifestPin{}
	imports, err := h.adoption.Imports(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, im := range imports {
		if !im.Disabled {
			guidance = append(guidance, ManifestPin{Name: im.Name, Version: im.Version})
		}
	}
	_, guidanceHash, _, err := h.adoption.Compile(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	policiesOut, err := h.adoption.ProjectPolicies(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if policiesOut == nil {
		policiesOut = []adoption.Policy{}
	}
	// bindings (existence + identity; live health lives on /integrations).
	// Secrets lists every store this AppTown can use now: the built-in store,
	// and Infisical when it is set up.
	bindings := []ManifestBinding{}
	for _, c := range []string{"scm", "secrets", "executor", "model", "deploy"} {
		var bs []integration.Binding
		if c == integration.CapSecrets {
			all, err := integration.ForCapability(r.Context(), h.db, c)
			if err != nil {
				WriteFault(w, err)
				return
			}
			available, err := h.stores.AvailableProviders()
			if err != nil {
				WriteFaultOr(w, err, http.StatusServiceUnavailable)
				return
			}
			bs = integration.WithProviders(all, available)
		} else if b, err := integration.Selected(r.Context(), h.db, c, ""); err == nil {
			bs = []integration.Binding{b}
		}
		for _, b := range bs {
			bindings = append(bindings, ManifestBinding{c, b.Name, b.Provider, b.Version, deploy.ConfigHash(b.ConfigJSON)})
		}
	}
	WriteJSON(w, 200, ProjectManifestView{
		Project: ManifestProject{Slug: p.Slug, Name: p.Name},
		Repo:    repo, Packs: packs, Workflows: workflows,
		Guidance: guidance, GuidanceHash: guidanceHash, Bindings: bindings,
		Policies: policiesOut,
	})
}

// ManifestPack is a pack applied to the project, and when.
type ManifestPack struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

// ManifestWorkflow is a workflow available to the project, with its origin.
type ManifestWorkflow struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Origin  string `json:"origin"`
}

// ManifestPin is a guidance block the project imports, at its pinned version.
type ManifestPin struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// ManifestBinding is an instance binding the project uses.
type ManifestBinding struct {
	Capability string `json:"capability"`
	Binding    string `json:"binding"`
	Provider   string `json:"provider"`
	Version    int    `json:"version"`
	ConfigHash string `json:"configHash"`
}

// ProjectManifestView is what a project is wired to: its repository, applied
// packs, workflows, pinned guidance, instance bindings and policies.
type ProjectManifestView struct {
	Project      ManifestProject    `json:"project"`
	Repo         ManifestRepo       `json:"repo"`
	Packs        []ManifestPack     `json:"packs"`
	Workflows    []ManifestWorkflow `json:"workflows"`
	Guidance     []ManifestPin      `json:"guidance"`
	GuidanceHash string             `json:"guidanceHash"`
	Bindings     []ManifestBinding  `json:"bindings"`
	Policies     []adoption.Policy  `json:"policies"`
}

// ManifestProject names the project.
type ManifestProject struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ManifestRepo says whether the project has a repository and where it points.
type ManifestRepo struct {
	Backed        bool   `json:"backed"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Remote        string `json:"remote,omitempty"`
}
