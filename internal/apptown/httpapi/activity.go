package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type Activity struct {
	work       *workflow.Service
	deployment *deployment.Service
}

func NewActivity(work *workflow.Service, deployment *deployment.Service) Activity {
	return Activity{work: work, deployment: deployment}
}

// Activity: GET /activity — what the whole instance is doing.
func (h Activity) Activity(w http.ResponseWriter, r *http.Request) {
	work, ctx := h.work, r.Context()
	// agents right now (running steps with elapsed)
	agents := []ActiveAgent{}
	running, err := work.RunningSteps(ctx)
	if err != nil {
		WriteFault(w, err)
		return
	}
	now := work.Now()
	for _, step := range running {
		row := ActiveAgent{Project: step.Project, RunID: step.RunID, ItemID: step.ChangeID, Title: step.Title, Step: step.Name, Type: step.Type}
		if step.StartedAt.Valid {
			row.ElapsedS = int(now.Sub(step.StartedAt.Time).Seconds())
		}
		agents = append(agents, row)
	}

	activeRuns, err := work.Runs(ctx, workflow.RunQuery{State: workflow.RunRunning, RootsOnly: true, Limit: 30})
	if err != nil {
		WriteFault(w, err)
		return
	}
	active := workflowRunRows(activeRuns, now)
	history, err := work.ChangeHistory(ctx, workflow.HistoryQuery{Limit: 10})
	if err != nil {
		WriteFault(w, err)
		return
	}

	// deployments across the instance
	envs := []ActivityDeployment{}
	runtime, err := h.deployment.Runtime(ctx)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, d := range runtime {
		e := ActivityDeployment{Project: d.Project, Name: d.Name, URL: d.URL, HostOnly: d.HostOnly}
		e.RuntimeObservationView = runtimeObservationView(d.Observation)
		e.Running = d.Observation.State.Running()
		envs = append(envs, e)
	}

	WriteJSON(w, 200, InstanceActivity{Agents: agents, Active: active, History: history, Deployments: envs})
}

// InstanceActivity is what the whole instance is doing: agents at work, runs
// under way, recent Changes and every deployment.
type (
	InstanceActivity struct {
		Agents      []ActiveAgent            `json:"agents"`
		Active      []runRowView             `json:"active"`
		History     []workflow.ChangeHistory `json:"history"`
		Deployments []ActivityDeployment     `json:"deployments"`
	}
	// ActiveAgent is a running step and how long it has run.
	ActiveAgent struct {
		Project  string `json:"project"`
		RunID    ids.ID `json:"runId"`
		ItemID   ids.ID `json:"changeId"`
		Title    string `json:"title"`
		Step     string `json:"step"`
		Type     string `json:"type"`
		ElapsedS int    `json:"elapsedS"`
	}
	// ActivityDeployment is one deployment and its last observed runtime.
	ActivityDeployment struct {
		Project string `json:"project"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		// HostOnly: URL answers on the AppTown host only (deployment.Reach).
		HostOnly bool  `json:"hostOnly,omitempty"`
		Running  *bool `json:"running"`
		RuntimeObservationView
	}
)
