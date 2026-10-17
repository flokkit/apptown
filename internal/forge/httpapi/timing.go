package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Where the time goes: per workflow, per
// step, across the runs of the last N days — how long a step takes, how
// long it waited to start, what it cost, how often it was reworked — and
// per run, the timeline. Read from the ledger the steps already write
// (started_at, finished_at, evidence usage); nothing new is recorded.

type stepTiming struct {
	Stage        string `json:"stage"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Runs         int    `json:"runs"`
	MedianMs     int64  `json:"medianMs"`
	P90Ms        int64  `json:"p90Ms"`
	MaxMs        int64  `json:"maxMs"`
	WaitMedianMs int64  `json:"waitMedianMs"` // from the previous step's end (or the run's birth) to this step's start; for an approval, the wait for a person
	CostMicros   int64  `json:"costMicros"`
	Reworks      int    `json:"reworks"` // times this step was sent back for another attempt, across the runs
}

type workflowTiming struct {
	Workflow      string       `json:"workflow"`
	Days          int          `json:"days"`
	Runs          int          `json:"runs"`
	TotalMedianMs int64        `json:"totalMedianMs"` // run birth to last step end
	Steps         []stepTiming `json:"steps"`
}

type timelineEntry struct {
	Stage      string `json:"stage"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Ms         int64  `json:"ms"`
	WaitMs     int64  `json:"waitMs"`
	CostMicros int64  `json:"costMicros"`
	reworks    int
}

// percentile of a sorted slice; 0.5 is the median.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// stepCostMicros reads an agent step's recorded cost from its evidence.
func stepCostMicros(evidenceJSON json.RawMessage) int64 {
	var ev struct {
		Usage struct {
			Cost struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(evidenceJSON, &ev) != nil {
		return 0
	}
	return int64(ev.Usage.Cost.Total * 1e6)
}

// timingTimeline lays recorded steps out in order, including waits for approval.
func timingTimeline(r workflow.RunTiming) []timelineEntry {
	prev := r.CreatedAt
	var out []timelineEntry
	for _, s := range r.Steps {
		e := timelineEntry{Stage: s.Stage, Name: s.Name, Type: s.Type, Status: s.Status.String(), CostMicros: stepCostMicros(s.Evidence), reworks: s.Reworks}
		start, end := s.StartedAt, s.FinishedAt
		switch {
		case s.Type == "approval" && end.Valid:
			// An approval has no start: its whole life is the wait for a person.
			e.FinishedAt = end.Time.UTC().Format(time.RFC3339Nano)
			if !prev.IsZero() {
				e.WaitMs = end.Time.Sub(prev).Milliseconds()
			}
			prev = end.Time
		case start.Valid:
			e.StartedAt = start.Time.UTC().Format(time.RFC3339Nano)
			if !prev.IsZero() {
				e.WaitMs = start.Time.Sub(prev).Milliseconds()
			}
			if end.Valid {
				e.FinishedAt = end.Time.UTC().Format(time.RFC3339Nano)
				e.Ms = end.Time.Sub(start.Time).Milliseconds()
				prev = end.Time
			}
		}
		if e.WaitMs < 0 {
			e.WaitMs = 0
		}
		out = append(out, e)
	}
	return out
}

// summarizeWorkflowTiming is pure: callers supply one consistent ledger snapshot.
func summarizeWorkflowTiming(name string, days int, runs []workflow.RunTiming) workflowTiming {
	type acc struct {
		st      stepTiming
		durs    []int64
		waits   []int64
		lastRun ids.ID
	}
	var order []*acc
	byKey := map[[2]string]*acc{}
	var totals []int64
	for _, run := range runs {
		id := run.ID
		tl := timingTimeline(run)
		born := run.CreatedAt
		var last time.Time
		for _, e := range tl {
			key := [2]string{e.Stage, e.Name}
			ac := byKey[key]
			if ac == nil {
				ac = &acc{st: stepTiming{Stage: e.Stage, Name: e.Name, Type: e.Type}}
				byKey[key] = ac
				order = append(order, ac)
			}
			if ac.lastRun != id {
				ac.st.Runs++
				ac.st.Reworks += e.reworks
				ac.lastRun = id
			}
			ac.st.CostMicros += e.CostMicros
			if e.Type == "approval" {
				if e.WaitMs > 0 {
					ac.waits = append(ac.waits, e.WaitMs)
				}
			} else {
				if e.FinishedAt != "" {
					ac.durs = append(ac.durs, e.Ms)
					ac.waits = append(ac.waits, e.WaitMs)
				}
			}
			if e.FinishedAt != "" {
				if t, err := time.Parse(time.RFC3339, e.FinishedAt); err == nil && t.After(last) {
					last = t
				}
			}
		}
		if !born.IsZero() && !last.IsZero() {
			totals = append(totals, last.Sub(born).Milliseconds())
		}
	}
	out := workflowTiming{Workflow: name, Days: days, Runs: len(runs)}
	for _, ac := range order {
		sort.Slice(ac.durs, func(i, j int) bool { return ac.durs[i] < ac.durs[j] })
		sort.Slice(ac.waits, func(i, j int) bool { return ac.waits[i] < ac.waits[j] })
		ac.st.MedianMs = percentile(ac.durs, 0.5)
		ac.st.P90Ms = percentile(ac.durs, 0.9)
		if n := len(ac.durs); n > 0 {
			ac.st.MaxMs = ac.durs[n-1]
		}
		ac.st.WaitMedianMs = percentile(ac.waits, 0.5)
		out.Steps = append(out.Steps, ac.st)
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
	out.TotalMedianMs = percentile(totals, 0.5)
	return out
}

// Summaries use the same runs as their timelines, busiest workflow first.
func summarizeTimings(runs []workflow.RunTiming, name string, days int) []workflowTiming {
	groups := map[string][]workflow.RunTiming{}
	for _, r := range runs {
		groups[r.Workflow] = append(groups[r.Workflow], r)
	}
	if name != "" {
		return []workflowTiming{summarizeWorkflowTiming(name, days, groups[name])}
	}
	out := []workflowTiming{}
	for name, group := range groups {
		out = append(out, summarizeWorkflowTiming(name, days, group))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Workflow < out[j].Workflow
	})
	return out
}

// Timing: GET /api/v1/timing?days=<n>[&workflow=<name>] — every
// workflow that ran in the window (or just the one named), step by step.
func (h Runs) Timing(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 7
	}
	name := r.URL.Query().Get("workflow")
	runs, err := h.work.Timings(r.Context(), workflow.TimingWindow{Workflow: name, Since: h.work.Now().UTC().AddDate(0, 0, -days)})
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := summarizeTimings(runs, name, days)

	WriteJSON(w, 200, TimingReport{Days: days, Workflows: out})
}

// RunTiming: GET /api/v1/runs/{id}/timing — one run's timeline.
func (h Runs) RunTiming(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad run id")
		return
	}
	runs, err := h.work.Timings(r.Context(), workflow.TimingWindow{RunID: &id})
	if err != nil {
		WriteFault(w, err)
		return
	}
	if len(runs) == 0 {
		WriteError(w, 404, "no such run")
		return
	}
	run := runs[0]
	WriteJSON(w, 200, RunTimeline{Run: run.ID, Workflow: run.Workflow, CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), Steps: timingTimeline(run)})
}

// TimingReport is every workflow that ran in the window, step by step.
type TimingReport struct {
	Days      int              `json:"days"`
	Workflows []workflowTiming `json:"workflows"`
}

// RunTimeline is one run's steps against the clock.
type RunTimeline struct {
	Run       ids.ID          `json:"run"`
	Workflow  string          `json:"workflow"`
	CreatedAt string          `json:"createdAt"`
	Steps     []timelineEntry `json:"steps"`
}
