package httpapi

import (
	"net/http"
	"strconv"
	"time"

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
	Rules       []workflow.StandingRevision    `json:"rules"`
	Projects    []JanitorProject               `json:"projects"`
	Deployments []deployment.JanitorDeployment `json:"deployments"`
	ReleaseHold string                         `json:"releaseHold"`
}

func (h Janitor) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.work.JanitorProjects(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	rules, err := h.standing.JanitorRules(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	rows := make([]JanitorProject, 0, len(projects))
	ids := make([]ids.ID, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ProjectID)
		project, err := h.schedules.Projects.ByID(r.Context(), p.ProjectID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		schedules, err := h.schedules.Of(r.Context(), project, h.now())
		if err != nil {
			WriteFault(w, err)
			return
		}
		row := JanitorProject{JanitorProject: p, JanitorSchedule: schedules.Janitor()}
		rows = append(rows, row)
	}
	deployments, err := h.releases.JanitorDeployments(r.Context(), ids)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, JanitorView{Rules: rules, Projects: rows, Deployments: deployments, ReleaseHold: deployment.JanitorReleaseHoldSentence()})
}

type JanitorHistoryView struct {
	History  workflow.JanitorHistory           `json:"history"`
	Releases []deployment.JanitorChangeRelease `json:"releases"`
	Since    time.Time                         `json:"since"`
	Until    time.Time                         `json:"until"`
	Offset   int                               `json:"offset"`
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
	history, err := h.work.LandedJanitorChanges(r.Context(), since, until, offset, 20)
	if err != nil {
		WriteFault(w, err)
		return
	}
	projects := []ids.ID{}
	changes := []ids.ID{}
	seen := map[ids.ID]bool{}
	for _, c := range history.Changes {
		changes = append(changes, c.ChangeID)
		if !seen[c.ProjectID] {
			projects = append(projects, c.ProjectID)
			seen[c.ProjectID] = true
		}
	}
	releases, err := h.releases.JanitorChangeReleases(r.Context(), projects, changes)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, JanitorHistoryView{History: history, Releases: releases, Since: since, Until: until, Offset: offset})
}
