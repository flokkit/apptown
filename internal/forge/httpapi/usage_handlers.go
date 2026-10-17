package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/usage"
)

type Usage struct {
	usage    *usage.Service
	projects *project.Projects
}

func NewUsage(usage *usage.Service, projects *project.Projects) Usage {
	return Usage{usage: usage, projects: projects}
}

// Usage: GET /api/v1/usage?project=slug&window=30&workflow=name — the
// caller's usage report. The usage owner decides what it covers: a named
// project needs read access to it; without one, the projects the caller can
// read, or the whole instance for an instance reader.
func (h Usage) Usage(w http.ResponseWriter, r *http.Request) {
	windowDays := 30
	if v := r.URL.Query().Get("window"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			windowDays = n
		}
	}
	var projectID ids.ID
	if slug := r.URL.Query().Get("project"); slug != "" {
		p, err := h.projects.BySlug(r.Context(), slug)
		if err != nil {
			WriteFault(w, err)
			return
		}
		projectID = p.ID
	}
	rep, err := h.usage.Report(r.Context(), usage.Query{WindowDays: windowDays, ProjectID: projectID, Workflow: r.URL.Query().Get("workflow")})
	if err != nil {
		WriteFault(w, err)
		return
	}
	byDayKind := []UsageDayKind{}
	for _, d := range rep.ByDayKind {
		byDayKind = append(byDayKind, UsageDayKind{Day: d.Day, Kind: d.Kind, Total: d.Total, CostMicros: d.CostMicros})
	}
	resp := UsageReport{
		WindowDays: rep.WindowDays, Totals: rep.Totals,
		ByKind:    usageLines(rep.ByKind, func(k string, c UsageCounts) UsageKindLine { return UsageKindLine{k, c} }),
		ByModel:   usageLines(rep.ByModel, func(k string, c UsageCounts) UsageModelLine { return UsageModelLine{k, c} }),
		ByDay:     usageLines(rep.ByDay, func(k string, c UsageCounts) UsageDayLine { return UsageDayLine{k, c} }),
		ByDayKind: byDayKind,
	}
	if !projectID.IsZero() {
		resp.ByWorkflow = usageLines(rep.ByWorkflow, func(k string, c UsageCounts) UsageWorkflowLine { return UsageWorkflowLine{k, c} })
	} else {
		resp.ByProject = usageLines(rep.ByProject, func(k string, c UsageCounts) UsageProjectLine { return UsageProjectLine{k, c} })
	}
	WriteJSON(w, 200, resp)
}

// usageLines turns a rollup into its labelled lines; never nil.
func usageLines[T any](ls []usage.Line, line func(key string, c UsageCounts) T) []T {
	out := make([]T, 0, len(ls))
	for _, l := range ls {
		out = append(out, line(l.Key, UsageCounts{Input: l.Input, Output: l.Output, Total: l.Total, CostMicros: l.CostMicros}))
	}
	return out
}

// UsageReport is the usage page over a window. A project's report breaks
// spend down by workflow; the instance's by project.
type UsageReport struct {
	WindowDays int                 `json:"windowDays"`
	Totals     usage.Totals        `json:"totals"`
	ByKind     []UsageKindLine     `json:"byKind"`
	ByModel    []UsageModelLine    `json:"byModel"`
	ByDay      []UsageDayLine      `json:"byDay"`
	ByDayKind  []UsageDayKind      `json:"byDayKind"`
	ByWorkflow []UsageWorkflowLine `json:"byWorkflow,omitzero"`
	ByProject  []UsageProjectLine  `json:"byProject,omitzero"`
}

// UsageCounts is one line's tokens and cost.
type UsageCounts struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Total      int64 `json:"total"`
	CostMicros int64 `json:"costMicros"`
}

type UsageKindLine struct {
	Kind string `json:"kind"`
	UsageCounts
}

type UsageModelLine struct {
	Model string `json:"model"`
	UsageCounts
}

type UsageDayLine struct {
	Day string `json:"day"`
	UsageCounts
}

type UsageWorkflowLine struct {
	Workflow string `json:"workflow"`
	UsageCounts
}

type UsageProjectLine struct {
	Project string `json:"project"`
	UsageCounts
}

// UsageDayKind is one day's spend of one kind, for the stacked chart.
type UsageDayKind struct {
	Day        string `json:"day"`
	Kind       string `json:"kind"`
	Total      int64  `json:"total"`
	CostMicros int64  `json:"costMicros"`
}
