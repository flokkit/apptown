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

type JanitorHistoryView struct {
	History workflow.JanitorHistory `json:"history"`
	Since   time.Time               `json:"since"`
	Until   time.Time               `json:"until"`
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
	WriteJSON(w, 200, JanitorHistoryView{History: history, Since: since, Until: until})
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
