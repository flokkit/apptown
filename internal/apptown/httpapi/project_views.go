package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/stamp"
)

type ProjectViews struct {
	projects   *project.Projects
	work       *workflow.Service
	adrs       *adrs.Service
	deployment *deployment.Service
	runner     execution.Runner
	mains      MainStandings
	dataDir    string
}

// MainStandings reads where a project's main stands against AppTown's record (scm.Branches).
type MainStandings interface {
	MainStanding(ctx context.Context, hub string, projectID ids.ID, branch string) (scm.MainStanding, error)
}

func NewProjectViews(projects *project.Projects, work *workflow.Service, adrs *adrs.Service, deployments *deployment.Service, runner execution.Runner, mains MainStandings, dataDir string) ProjectViews {
	return ProjectViews{projects: projects, work: work, adrs: adrs, deployment: deployments, runner: runner, mains: mains, dataDir: dataDir}
}
func (h ProjectViews) ListProjects(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	paged := query.Has("limit") || query.Has("offset") || query.Has("q")
	var projects []project.Project
	var total int64
	var err error
	if query.Has("recent") {
		limit, parseErr := strconv.Atoi(query.Get("recent"))
		if parseErr != nil || limit < 1 || limit > 100 || paged {
			WriteError(w, 400, "recent must be 1–100 without page parameters")
			return
		}
		idList, recentErr := h.work.RecentProjects(r.Context(), limit)
		if recentErr != nil {
			WriteFault(w, recentErr)
			return
		}
		if idList == nil {
			idList = []ids.ID{}
		}
		page, pageErr := h.projects.Browse(r.Context(), project.Browse{Limit: limit, IDs: idList})
		projects, err = page.Projects, pageErr
	} else if paged {
		limit, offset := 50, 0
		if query.Has("limit") {
			limit, err = strconv.Atoi(query.Get("limit"))
		}
		if err == nil && query.Has("offset") {
			offset, err = strconv.Atoi(query.Get("offset"))
		}
		if err != nil || limit < 1 || limit > 100 || offset < 0 {
			WriteError(w, 400, "limit must be 1–100 and offset nonnegative")
			return
		}
		page, pageErr := h.projects.Browse(r.Context(), project.Browse{Limit: limit, Offset: offset, Query: query.Get("q")})
		projects, total, err = page.Projects, page.Total, pageErr
	} else {
		projects, err = h.projects.Visible(r.Context())
	}
	if err != nil {
		WriteFault(w, err)
		return
	}
	idList := make([]ids.ID, 0, len(projects))
	for _, p := range projects {
		idList = append(idList, p.ID)
	}
	activity, err := h.work.ProjectActivityFor(r.Context(), idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []ProjectRow{}
	for _, p := range projects {
		work := activity[p.ID]
		out = append(out, ProjectRow{Project: p, ActiveItems: work.ActiveItems, DoneRuns: work.DoneRuns, LastRunAt: work.LastRunAt})
	}
	counts, err := h.adrs.Counts(r.Context(), idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		out[i].AdrCount = counts[out[i].ID]
	}
	runtime, err := h.deployment.ProjectRuntime(r.Context(), idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		observed, ok := runtime[out[i].ID]
		if !ok {
			observed.Stale = true
		}
		out[i].Deployed = ok
		out[i].ProdRunning = observed.State.Running()
		out[i].RuntimeObservationView = runtimeObservationView(observed)
		out[i].Toolchain = readToolchainNames(filepath.Join(h.dataDir, "workspaces", "project-"+out[i].Slug))
	}
	if paged {
		WriteJSON(w, 200, ProjectPage{Projects: out, Total: total})
		return
	}
	WriteJSON(w, 200, out)
}

func (h ProjectViews) GetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	lifecycle, err := h.projects.Lifecycle(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	// The runtime form is a load-bearing property — it decides what can
	// happen to the project (previewable? intranet-releasable?) — and it
	// was invisible until a red line at the release door. Surface
	// it on the project itself, in plain words.
	form, releasable := "no app yet", false
	if c, err := docker.ReadWorkspaceContract(filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)); err == nil {
		switch {
		case c.PublicPart().Static != "":
			_, err := docker.SelectedPath(filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug), c.PublicPart().Static)
			form, releasable = "static site", err == nil
		case c.PublicPart().StaticBuild != nil:
			_, err := docker.SelectedPath(filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug), filepath.Join(c.PublicPart().StaticBuild.Context, "Dockerfile"))
			form, releasable = "static site", err == nil
		case c.PublicPart().Image != "":
			form, releasable = "ships as a container", true
		case c.PublicPart().Build != "":
			_, err := docker.SelectedPath(filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug), filepath.Join(c.PublicPart().Build, "Dockerfile"))
			form, releasable = "runs from source", err == nil
		}
	}
	// Out-of-band edits (gitrepo.go repoDrift): shown to everyone —
	// tampering hides best when only the tamperer knows to look.
	hub := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
	var drift []string
	if hasGitDirectory(hub) {
		var err error
		drift, err = scm.Drift(r.Context(), h.runner, hub)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	// Where main stands against AppTown's record: a note, never an error.
	var main *scm.MainStanding
	// A repository-backed project is always assessed, even with its hub
	// missing: that reads as "could not read main", never as nothing.
	if p.DefaultBranch != "" {
		standing, err := h.mains.MainStanding(r.Context(), hub, p.ID, p.DefaultBranch)
		if err != nil {
			WriteFault(w, err)
			return
		}
		main = &standing
	}
	WriteJSON(w, 200, ProjectDetail{
		ID: p.ID, Slug: p.Slug, Name: p.Name, Namespace: p.Namespace,
		Icon: p.Icon, Blurb: p.Blurb, DefaultBranch: p.DefaultBranch,
		CreatedAt:   p.CreatedAt,
		RuntimeForm: form, IntranetReady: releasable,
		OutOfBandEdits:             drift,
		Main:                       main,
		RequireIndependentApproval: p.RequireIndependentApproval,
		SettingsRevision:           p.SettingsRevision,
		Lifecycle:                  lifecycle,
	})
}

// readToolchainNames lists the tool names a repo's mise.toml declares —
// display metadata only.
func readToolchainNames(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "mise.toml"))
	if err != nil {
		return nil
	}
	var names []string
	inTools := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inTools = t == "[tools]"
			continue
		}
		if inTools {
			if k, _, ok := strings.Cut(t, "="); ok {
				if name := strings.TrimSpace(k); name != "" && !strings.HasPrefix(name, "#") {
					names = append(names, name)
				}
			}
		}
	}
	return names
}

func hasGitDirectory(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// ProjectRow is a project in the projects list, with its activity and what
// is running.
type ProjectRow struct {
	project.Project
	ActiveItems int        `json:"activeItems"`
	AdrCount    int        `json:"adrCount"`
	DoneRuns    int        `json:"doneRuns"`
	LastRunAt   stamp.Time `json:"lastRunAt"`
	Toolchain   []string   `json:"toolchain"`
	ProdRunning *bool      `json:"prodRunning"`
	// Deployed says the project has a main (or prod) deployment for the
	// runtime observation to describe. False is "not deployed yet", which
	// a page must not show as an unknown or a stopped app.
	Deployed bool `json:"deployed"`
	RuntimeObservationView
}

// ProjectPage is one page of the projects list.
type ProjectPage struct {
	Projects []ProjectRow `json:"projects"`
	Total    int64        `json:"total"`
}

// ProjectDetail is one project: what it is, what form its app takes, and any
// files changed outside AppTown.
type ProjectDetail struct {
	ID             ids.ID   `json:"id"`
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	Namespace      string   `json:"namespace"`
	Icon           string   `json:"icon"`
	Blurb          string   `json:"blurb"`
	DefaultBranch  string   `json:"defaultBranch"`
	CreatedAt      string   `json:"createdAt"`
	RuntimeForm    string   `json:"runtimeForm"`
	IntranetReady  bool     `json:"intranetReady"`
	OutOfBandEdits []string `json:"outOfBandEdits"`
	// Main compares the hub's main with where AppTown last recorded it;
	// absent for a project without a repository.
	Main                       *scm.MainStanding `json:"main,omitempty"`
	RequireIndependentApproval bool              `json:"requireIndependentApproval"`
	// SettingsRevision is what an edit of the name and face sends back.
	SettingsRevision int64 `json:"settingsRevision"`
	// Lifecycle is where the project stands: active, or being imported,
	// being deleted, or deleted and kept until a date.
	Lifecycle project.Lifecycle `json:"lifecycle"`
}
