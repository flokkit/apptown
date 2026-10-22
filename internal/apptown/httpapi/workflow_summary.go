package httpapi

import (
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/strs"
)

// Workflow summaries: the plain-language reading of a workflow, computed
// server-side from the SAME parser the engine runs (schema.ParseWorkflow),
// so the story and the machine can't drift. Consumed by the workflow list,
// the workflow read page, and the CLI — the layering principle
// (lead with the plain sentence): a workflow leads with what happens in ordinary
// words; the YAML stays one fold away.

type workflowStepSummary struct {
	Name  string `json:"name"`
	Uses  string `json:"uses"`
	Icon  string `json:"icon"`
	Gloss string `json:"gloss"`
	Human bool   `json:"human,omitempty"` // a person's moment (approval)
	// Means: the stated standard of approval — what an OK asserts here.
	Means string `json:"means,omitempty"`
}

type workflowStageSummary struct {
	Name     string                `json:"name"`
	Steps    []workflowStepSummary `json:"steps"`
	GateText string                `json:"gateText"`
}

type workflowSummary struct {
	Description string                 `json:"description,omitempty"`
	Pacing      string                 `json:"pacing"` // "paced by people" | "runs straight through"
	Trigger     string                 `json:"trigger,omitempty"`
	Flow        string                 `json:"flow"` // the one-line story for lists
	Stages      []workflowStageSummary `json:"stages"`
}

func glossLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strs.Truncate(strings.TrimSpace(s), max)
}

func stepSummary(s schema.Step) workflowStepSummary {
	w := func(k string) string {
		if s.With == nil {
			return ""
		}
		v, _ := s.With[k].(string)
		return v
	}
	out := workflowStepSummary{Name: s.Name, Uses: s.Uses}
	switch s.Uses {
	case "agent/pi":
		// The prompt is written to the agent, not to the person reading the
		// workflow; its first line is an instruction fragment, so it is not
		// quoted here. The step's name and the workflow's description say
		// what the step is for.
		out.Icon, out.Gloss = "🤖", "an agent works on it"
	case "approval":
		out.Icon, out.Human = "👍", true
		out.Gloss = "waits for a person's OK"
		out.Means = glossLine(w("means"), 160)
		quorum := 1
		switch q := s.With["quorum"].(type) {
		case int:
			quorum = q
		case float64:
			quorum = int(q)
		}
		if apps, ok := s.With["approvers"].([]any); ok && len(apps) > 0 {
			names := make([]string, 0, len(apps))
			for _, a := range apps {
				if n, ok := a.(string); ok {
					if g, isGroup := strings.CutPrefix(n, "group:"); isGroup {
						n = "the " + g + " group"
					}
					names = append(names, n)
				}
			}
			if len(names) > 0 && quorum > 1 {
				out.Gloss = fmt.Sprintf("waits for OKs from %d people (%s)", quorum, strings.Join(names, " or "))
			} else if len(names) > 0 {
				out.Gloss = "waits for an OK from " + strings.Join(names, " or ")
			}
		} else if quorum > 1 {
			out.Gloss = fmt.Sprintf("waits for OKs from %d people", quorum)
		}
	case "script":
		out.Icon, out.Gloss = "⚙️", "runs a command"
		if c := glossLine(w("run"), 70); c != "" {
			out.Gloss = "runs: " + c
		}
	case "artifact/require":
		out.Icon = "📋"
		if k := w("kind"); k != "" {
			out.Gloss = fmt.Sprintf("checks a %s exists from an earlier step", k)
		} else {
			out.Gloss = "checks a required artifact exists"
		}
	case "adr/require":
		out.Icon, out.Gloss = "📋", "checks an accepted decision record (ADR) exists"
	case "scm/mr":
		out.Icon, out.Gloss = "🔀", "pushes the branch and opens a merge request"
	case "preview":
		out.Icon, out.Gloss = "▶", "starts the app so it can be tried"
	case "deploy":
		out.Icon, out.Gloss = "🚀", "updates the running app (main deployment)"
		if e := w("deployment"); e != "" && e != "main" {
			out.Gloss = "updates the running app (" + e + " deployment)"
		}
	case "scan":
		out.Icon, out.Gloss = "🛡", "security scan of the code"
	case "tests":
		out.Icon, out.Gloss = "🧪", "runs the app's tests; a failure stops the run"
	case "agent/review":
		out.Icon, out.Gloss = "🔍", "a second agent checks the report against the diff"
	case "guidance/conformance":
		out.Icon, out.Gloss = "🧭", "checks the work against the project's guidance"
	case "workflow/call":
		out.Icon, out.Gloss = "↪", "runs another workflow"
		if n := w("workflow"); n != "" {
			out.Gloss = "runs the " + n + " workflow"
		}
	default:
		out.Icon, out.Gloss = "•", s.Uses
	}
	return out
}

// summarizeWorkflow turns YAML into the plain reading. Returns nil when the
// YAML doesn't parse — the caller shows what it has and the editor's
// validator owns the error story.
func summarizeWorkflow(yamlSrc string) *workflowSummary {
	doc, err := schema.ParseWorkflowStored([]byte(yamlSrc))
	if err != nil {
		return nil
	}
	sum := &workflowSummary{Description: doc.Metadata.Description}
	if doc.Spec.Kind == "pipeline" {
		sum.Pacing = "runs straight through"
	} else {
		sum.Pacing = "paced by people"
	}
	if doc.Spec.On.Schedule != nil {
		sum.Trigger = "also runs on a schedule (" + doc.Spec.On.Schedule.Cron + ")"
	}
	var flow []string
	for _, st := range doc.Spec.Stages {
		stage := workflowStageSummary{Name: st.Name}
		all := append(append([]schema.Step{}, st.OnEnter...), st.Steps...)
		for _, s := range all {
			ss := stepSummary(s)
			stage.Steps = append(stage.Steps, ss)
			if len(flow) < 9 {
				flow = append(flow, ss.Icon+" "+ss.Name)
			} else if len(flow) == 9 {
				flow = append(flow, "…")
			}
		}
		if st.Gate != nil && len(st.Gate.Require) > 0 && len(st.Gate.Require) < len(all) {
			stage.GateText = "moves on when these pass: " + strings.Join(st.Gate.Require, ", ")
		} else {
			stage.GateText = "moves on when every step here has passed"
		}
		sum.Stages = append(sum.Stages, stage)
	}
	if n := len(sum.Stages); n > 0 {
		sum.Stages[n-1].GateText = strings.Replace(sum.Stages[n-1].GateText, "moves on", "the run finishes", 1)
	}
	sum.Flow = strings.Join(flow, " → ")
	return sum
}
