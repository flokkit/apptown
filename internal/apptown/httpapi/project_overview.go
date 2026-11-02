package httpapi

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/status"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
)

type ProjectOverview struct {
	projects *project.Projects
	work     *workflow.Service
	dataDir  string
}

func NewProjectOverview(projects *project.Projects, work *workflow.Service, dataDir string) ProjectOverview {
	return ProjectOverview{projects: projects, work: work, dataDir: dataDir}
}

// ProjectOverview: GET /projects/{slug}/overview
func (h ProjectOverview) ProjectOverview(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}

	work, ctx := h.work, r.Context()

	// repo card
	var repo *OverviewRepo
	if p.DefaultBranch != "" {
		dir, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		remote, _, hasRemote := scm.Origin(ctx, dir)
		sha, _ := gitexec.Run(ctx, dir, "rev-parse", "--short", "HEAD")
		subject, _ := gitexec.Run(ctx, dir, "log", "-1", "--format=%s")
		when, _ := gitexec.Run(ctx, dir, "log", "-1", "--format=%cr")
		commits, _ := gitexec.Run(ctx, dir, "rev-list", "--count", "HEAD")
		repo = &OverviewRepo{
			DefaultBranch: p.DefaultBranch,
			Path:          dir,
			Head:          OverviewHead{SHA: sha, Subject: subject, When: when},
			Commits:       commits,
		}
		if hasRemote {
			clean := scm.PublicRemoteURL(remote)
			repo.Remote = clean
			repo.CloneCmd = "git clone " + clean
			repo.WebURL = strings.TrimSuffix(clean, ".git")
		}
	}

	activeRuns, err := work.Runs(ctx, workflow.RunQuery{ProjectID: p.ID, State: workflow.RunRunning, RootsOnly: true})
	if err != nil {
		WriteFault(w, err)
		return
	}
	active := workflowRunRows(activeRuns, work.Now())
	// History is CHANGE history: one row per finished
	// change — its workflows, its outcome, its total wall time. Runs are the
	// how; the change page holds them for the drill-in.
	changes, err := work.ChangeHistory(ctx, workflow.HistoryQuery{ProjectID: p.ID, Limit: 30})
	if err != nil {
		WriteFault(w, err)
		return
	}

	// Open changes WITHOUT a running run — chat changes mid-edit, accepted
	// work between runs — belong here too, or "Happening now" says nothing
	// while a change sits open.
	open := []OpenChange{}
	available, err := work.OpenChangesWithoutRuns(ctx, p.ID, 10)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, item := range available {
		oc := OpenChange{ChangeID: item.ID, ChangeNumber: item.Number, Title: item.Title, State: item.State}
		oc.Editing = item.Editing
		oc.StatusLabel = oc.State.OpenDisplay(oc.Editing)
		open = append(open, oc)
	}

	WriteJSON(w, 200, ProjectOverviewView{Repo: repo, Active: active, Open: open, History: changes})
}

// OpenChange is an open change with no running run, and how many files it is
// editing.
type OpenChange struct {
	StatusLabel  status.DisplayStatus `json:"statusLabel"`
	ChangeID     ids.ID               `json:"changeId"`
	ChangeNumber int64                `json:"changeNumber"` // within the project
	Title        string               `json:"title"`
	State        workflow.ChangeState `json:"state"`
	Editing      int                  `json:"editing"` // uncommitted worktree files
}

// ProjectOverviewView is a project's front page: its repository, what is
// running, what is open, and finished changes.
type ProjectOverviewView struct {
	Repo    *OverviewRepo            `json:"repo"`
	Active  []runRowView             `json:"active"`
	Open    []OpenChange             `json:"open"`
	History []workflow.ChangeHistory `json:"history"`
}

// OverviewRepo is the project's repository card.
type OverviewRepo struct {
	DefaultBranch string       `json:"defaultBranch"`
	Path          string       `json:"path"`
	Head          OverviewHead `json:"head"`
	Commits       string       `json:"commits"`
	Remote        string       `json:"remote,omitempty"`
	CloneCmd      string       `json:"cloneCmd,omitempty"`
	WebURL        string       `json:"webUrl,omitempty"`
}

// OverviewHead is the repository's latest commit.
type OverviewHead struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}
