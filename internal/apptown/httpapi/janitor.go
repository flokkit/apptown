package httpapi

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

type Janitor struct {
	schedules changeactions.Schedules
	work      *workflow.Service
	standing  workflow.StandingRules
	releases  *deployment.Service
	now       func() time.Time
}

func NewJanitor(schedules changeactions.Schedules, work *workflow.Service, standing workflow.StandingRules, releases *deployment.Service, now func() time.Time) Janitor {
	return Janitor{schedules: schedules, work: work, standing: standing, releases: releases, now: now}
}

type JanitorProject struct {
	workflow.JanitorProject
	changeactions.JanitorSchedule
}

type JanitorView struct {
	Unknown     []ids.ID                       `json:"unknown"`
	Choices     *workflow.StandingRevision     `json:"choices"`
	Projects    []JanitorProject               `json:"projects"`
	Deployments []deployment.JanitorDeployment `json:"deployments"`
}

func (h Janitor) List(w http.ResponseWriter, r *http.Request) {
	projects, choices, unknown, err := h.work.JanitorProjects(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	schedules, err := h.schedules.ForJanitor(r.Context(), projects, h.now())
	if err != nil {
		WriteFault(w, err)
		return
	}
	rows := make([]JanitorProject, 0, len(projects))
	ids := make([]ids.ID, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ProjectID)
		row := JanitorProject{JanitorProject: p, JanitorSchedule: schedules[p.ProjectID].Janitor()}
		rows = append(rows, row)
	}
	deployments, err := h.releases.JanitorDeployments(r.Context(), ids)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, JanitorView{Unknown: unknown, Choices: choices, Projects: rows, Deployments: deployments})
}

type janitorLandedChange struct {
	ReleaseID     ids.ID                 `json:"releaseId,omitzero"`
	ReleaseNumber int64                  `json:"releaseNumber,omitempty"`
	Slug          string                 `json:"slug,omitempty"`
	Project       string                 `json:"project"`
	PackVersions  []adoption.PackPick    `json:"packVersions"`
	ChangeID      ids.ID                 `json:"changeId"`
	ProjectID     ids.ID                 `json:"projectId"`
	Title         string                 `json:"title"`
	LandedAt      time.Time              `json:"landedAt"`
	Queue         string                 `json:"queue"`
	Packages      []workflow.PackageMove `json:"packages"`
}
type janitorHistory struct {
	Changes []janitorLandedChange `json:"changes"`
	More    bool                  `json:"more"`
}

type JanitorHistoryView struct {
	History janitorHistory `json:"history"`
	Since   time.Time      `json:"since"`
	Until   time.Time      `json:"until"`
}

func (h Janitor) History(w http.ResponseWriter, r *http.Request) {
	now := h.now().UTC()
	until := now.Add(time.Nanosecond)
	since := until.Add(-7 * 24 * time.Hour)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		until, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			WriteError(w, 400, "bad history boundary")
			return
		}
		since = until.Add(-7 * 24 * time.Hour)
	}
	if r.URL.Query().Get("period") == "older" {
		since = time.Unix(0, 0).UTC()
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			WriteError(w, 400, "bad history offset")
			return
		}
	}
	landed, err := h.work.LandedJanitorChanges(r.Context(), since, until, 0, offset+21)
	if err != nil {
		WriteFault(w, err)
		return
	}
	history := janitorHistory{Changes: []janitorLandedChange{}, More: landed.More}
	for _, c := range landed.Changes {
		history.Changes = append(history.Changes, janitorLandedChange{Project: c.Project, PackVersions: c.PackVersions, ChangeID: c.ChangeID, ProjectID: c.ProjectID, Title: c.Title, LandedAt: c.LandedAt, Queue: c.Queue, Packages: c.Packages})
	}
	updates, err := h.releases.RecentNeedMoves(r.Context(), since, until, offset+21)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, u := range updates {
		item := janitorLandedChange{ReleaseID: u.ID, ReleaseNumber: u.Number, Slug: u.Slug, ProjectID: u.ProjectID, Project: u.Project, LandedAt: u.At, Packages: []workflow.PackageMove{}, PackVersions: []adoption.PackPick{}}
		for _, move := range u.Moves {
			item.Packages = append(item.Packages, workflow.PackageMove{Package: move.Need, From: move.From, To: move.To})
		}
		history.Changes = append(history.Changes, item)
	}
	slices.SortFunc(history.Changes, func(a, b janitorLandedChange) int {
		if n := b.LandedAt.Compare(a.LandedAt); n != 0 {
			return n
		}
		return cmp.Compare(a.Title, b.Title)
	})
	history.More = history.More || len(history.Changes) > offset+20
	history.Changes = history.Changes[min(offset, len(history.Changes)):min(offset+20, len(history.Changes))]
	WriteJSON(w, 200, JanitorHistoryView{History: history, Since: since, Until: until})
}

// Statistics: GET /janitor/statistics?days=7|30. Workflow owns the period
// rule, the counts and the clock; this only reads the query.
func (h Janitor) Statistics(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		WriteError(w, 400, "the period is 7 or 30 days")
		return
	}
	stats, err := h.work.JanitorStatistics(r.Context(), days)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, stats)
}

// JanitorRunView reports each queue independently, including ordinary refusals.
type JanitorRunView struct {
	Starts []changeactions.JanitorStart `json:"starts"`
}

func (h Janitor) Run(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.schedules.Projects, w, r)
	if !ok {
		return
	}
	jsonCommand[changeactions.JanitorRun](w, r, jsonBody{strict: true}, func(req changeactions.JanitorRun) {
		starts, err := h.schedules.RunJanitor(r.Context(), *p, req, h.now())
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, JanitorRunView{Starts: starts})
	})
}
