package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

type Janitor struct {
	work     *workflow.Service
	standing workflow.StandingRules
	releases *deployment.Service
	now      func() time.Time
}

func NewJanitor(work *workflow.Service, standing workflow.StandingRules, releases *deployment.Service, now func() time.Time) Janitor {
	return Janitor{work: work, standing: standing, releases: releases, now: now}
}

type janitorChoice struct {
	Level  string       `json:"level"`
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
	Reason string       `json:"reason"`
}

type janitorProcedure struct {
	Workflow     string `json:"workflow"`
	Version      int    `json:"version"`
	RepairRounds int    `json:"repairRounds"`
	StopRule     string `json:"stopRule"`
	Sentence     string `json:"sentence"`
	Problem      string `json:"problem"`
}

type JanitorProject struct {
	ProjectID      ids.ID                 `json:"projectId"`
	Slug           string                 `json:"slug"`
	Name           string                 `json:"name"`
	Enabled        bool                   `json:"enabled"`
	Level          string                 `json:"level"`
	Choice         *janitorChoice         `json:"choice,omitempty"`
	Issuer         access.Actor           `json:"issuer"`
	GrantedAt      time.Time              `json:"grantedAt"`
	Queues         []workflow.QueueStatus `json:"queues"`
	Scans          workflow.MainScans     `json:"scans"`
	Problem        string                 `json:"problem"`
	Procedures     []janitorProcedure     `json:"procedures"`
	QueueSentences []string               `json:"queueSentences"`
	NextUpdate     time.Time              `json:"nextUpdate"`
	NextReason     string                 `json:"nextReason"`
	// ScanNow is why the scan schedule cannot be fired now; "" when it can.
	ScanNow string `json:"scanNow"`
}

type janitorLandedChange struct {
	Rule *struct {
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	} `json:"rule,omitempty"`
	Slug         string              `json:"slug"`
	Project      string              `json:"project"`
	PackVersions []adoption.PackPick `json:"packVersions"`
	ChangeID     ids.ID              `json:"changeId"`
	ProjectID    ids.ID              `json:"projectId"`
	Number       int64               `json:"number"`
	Title        string              `json:"title"`
	LandedAt     time.Time           `json:"landedAt"`
	Queue        string              `json:"queue"`
	Packages     []fix.Move          `json:"packages"`
	RuleID       ids.ID              `json:"ruleId,omitzero"`
	RuleRevision int64               `json:"ruleRevision"`
	Issuer       access.Actor        `json:"issuer"`
	GrantedAt    time.Time           `json:"grantedAt"`
	Schedule     string              `json:"schedule"`
}

type JanitorHistory struct {
	Changes []janitorLandedChange `json:"changes"`
	More    bool                  `json:"more"`
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
		row := JanitorProject{ProjectID: p.ProjectID, Slug: p.Slug, Name: p.Name, Enabled: p.Enabled, Level: p.Level, Issuer: p.Issuer, GrantedAt: p.GrantedAt, Queues: p.Queues, Scans: p.Scans, Problem: p.Problem, QueueSentences: p.QueueSentences, NextUpdate: p.NextUpdate, NextReason: p.NextReason, ScanNow: p.ScanNow, Procedures: []janitorProcedure{}}
		if c := p.Choice; c != nil {
			row.Choice = &janitorChoice{Level: c.Level, Person: c.Person, At: c.At, Reason: c.Reason}
		}
		for _, proc := range p.Procedures {
			row.Procedures = append(row.Procedures, janitorProcedure(proc))
		}
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
	History  JanitorHistory                    `json:"history"`
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
	view := JanitorHistory{More: history.More, Changes: []janitorLandedChange{}}
	for _, c := range history.Changes {
		view.Changes = append(view.Changes, janitorLandedChange(c))
	}
	WriteJSON(w, 200, JanitorHistoryView{History: view, Releases: releases, Since: since, Until: until, Offset: offset})
}
