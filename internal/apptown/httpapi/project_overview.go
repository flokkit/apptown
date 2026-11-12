package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/projectnotes"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/stamp"
)

type ProjectOverview struct {
	projects *project.Projects
	work     *workflow.Service
	dataDir  string
	features *identity.Service
}

func NewProjectOverview(projects *project.Projects, work *workflow.Service, dataDir string, features *identity.Service) ProjectOverview {
	return ProjectOverview{projects: projects, work: work, dataDir: dataDir, features: features}
}

// ProjectOverview: GET /projects/{slug}/overview
func (h ProjectOverview) ProjectOverview(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}

	work, ctx := h.work, r.Context()
	memory, err := h.features.FeatureState(ctx, "memory")
	if err != nil {
		WriteFault(w, err)
		return
	}

	// repo card
	var repo *OverviewRepo
	var notes *ProjectNotesView
	if p.DefaultBranch != "" {
		dir, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if memory.Enabled {
			notes = &ProjectNotesView{Notes: []ProjectNoteView{}}
			commit, readErr := gitexec.Run(ctx, dir, "rev-parse", "--verify", "refs/heads/"+p.DefaultBranch+"^{commit}")
			if readErr != nil {
				notes.Error = "Could not read project notes: " + readErr.Error()
			} else {
				notes.Commit = commit
				parsed, present, readErr := projectnotes.Read(ctx, dir, commit)
				notes.Present = present
				if readErr != nil {
					notes.Error = readErr.Error()
				} else {
					sources := map[int64]ProjectNoteView{}
					for _, n := range parsed {
						source, ok := sources[n.Change]
						if !ok {
							acceptance, e := work.AcceptanceByNumber(ctx, p.ID, p.Slug, n.Change)
							source = ProjectNoteView{ChangeID: acceptance.ChangeID, AcceptedBy: acceptance.AcceptedBy}
							if e != nil {
								source.SourceError = "Source acceptance unavailable"
							}
							sources[n.Change] = source
						}
						source.Note = n
						notes.Notes = append(notes.Notes, source)
					}
				}
			}
		}
		remote, _, hasRemote := scm.Origin(ctx, dir)
		sha, _ := gitexec.Run(ctx, dir, "rev-parse", "--short", "HEAD")
		subject, _ := gitexec.Run(ctx, dir, "log", "-1", "--format=%s")
		when, _ := gitexec.Run(ctx, dir, "log", "-1", "--format=%cI")
		commits, _ := gitexec.Run(ctx, dir, "rev-list", "--count", "HEAD")
		repo = &OverviewRepo{
			DefaultBranch: p.DefaultBranch,
			Path:          dir,
			Head:          OverviewHead{SHA: sha, Subject: subject, When: stamp.Parse(when)},
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
		oc := OpenChange{WaitingOn: item.WaitingOn, ChangeID: item.ID, ChangeNumber: item.Number, Title: item.Title, State: item.State}
		oc.Editing = item.Editing
		open = append(open, oc)
	}

	WriteJSON(w, 200, ProjectOverviewView{Notes: notes, Repo: repo, Active: active, Open: open, History: changes})
}

// OpenChange is an open change with no running run, and how many files it is
// editing.
type OpenChange struct {
	WaitingOn    workflow.WaitingOn   `json:"waitingOn"`
	ChangeID     ids.ID               `json:"changeId"`
	ChangeNumber int64                `json:"changeNumber"` // within the project
	Title        string               `json:"title"`
	State        workflow.ChangeState `json:"state"`
	Editing      int                  `json:"editing"` // uncommitted worktree files
}

// ProjectOverviewView is a project's front page: its repository, what is
// running, what is open, and finished changes.
type ProjectNoteView struct {
	projectnotes.Note
	ChangeID    ids.ID   `json:"changeId"`
	AcceptedBy  []string `json:"acceptedBy"`
	SourceError string   `json:"sourceError,omitempty"`
}
type ProjectNotesView struct {
	Present bool              `json:"present"`
	Commit  string            `json:"commit"`
	Notes   []ProjectNoteView `json:"notes"`
	Error   string            `json:"error,omitempty"`
}
type ProjectOverviewView struct {
	Notes *ProjectNotesView `json:"notes"`

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
	// When is the commit's own committer time (AppTown dates its merges by
	// its own clock); the page formats it as every other event time.
	When stamp.Time `json:"when"`
}
