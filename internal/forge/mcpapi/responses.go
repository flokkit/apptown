package mcpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/internal/strs"
)

func (c *Call) projectURL(slug string) string { return c.BaseURL + "/projects/" + slug }
func (c *Call) changeURL(id ids.ID) string    { return fmt.Sprintf("%s/changes/%d", c.BaseURL, id) }
func (c *Call) runURL(run *workflow.Run, slug string) string {
	if !run.ChangeID.IsZero() {
		return c.changeURL(run.ChangeID)
	}
	return c.projectURL(slug)
}
func (c *Call) mcpRunSummary(d *workflow.RunSnapshot, slug string) map[string]any {
	steps := make([]mcpStep, 0, len(d.Steps))
	for _, s := range d.Steps {
		steps = append(steps, mcpStepOf(s))
	}
	out := map[string]any{
		"id": d.Run.ID, "workflow": d.Run.WorkflowName, "workflowVersion": d.Run.WorkflowVersion,
		"status": d.Run.Status, "currentStage": d.Run.CurrentStage, "stages": d.Stages,
		"changeId": d.Run.ChangeID, "createdAt": d.Run.CreatedAt, "updatedAt": d.Run.UpdatedAt,
		"steps": steps, "webUrl": c.runURL(d.Run, slug),
	}
	if d.State != "" {
		out["state"], out["stateDetail"] = d.State, d.StateDetail
	}
	out["waiting"] = mcpWaiting(d)
	return out
}

// mcpWaiting says what the run waits on, from Work's own reading: the
// approval Work offers, a running or stopped step of the stage, and otherwise
// the run's activity. "Ready to advance" only when Work offers to advance; an
// empty offer is not readiness.
func mcpWaiting(d *workflow.RunSnapshot) map[string]any {
	if d.Run.Status != workflow.RunRunning {
		return map[string]any{"on": "nothing", "why": "the run is " + d.Run.Status.String()}
	}
	advance := false
	for _, act := range d.Next {
		advance = advance || act.Act == workflow.NextAdvance
		if act.Act != workflow.NextApprove {
			continue
		}
		means := ""
		for _, s := range d.Steps {
			if s.ID == act.StepID {
				means, _ = parseJSONMap(string(s.Config))["means"].(string)
			}
		}
		return map[string]any{"on": "a person", "step": act.Step, "stepId": act.StepID, "means": means,
			"why": "an approval is pending"}
	}
	for _, s := range d.Steps {
		if s.Stage != d.Run.CurrentStage {
			continue
		}
		switch {
		case s.Status == workflow.StatusRunning:
			who := "the forge"
			if s.Type == "agent/pi" {
				who = "an agent"
			}
			return map[string]any{"on": who, "step": s.Name, "stepId": s.ID, "why": s.Name + " is running"}
		case s.Status == workflow.StatusFail || s.Status == workflow.StatusError:
			return map[string]any{"on": "a fix", "step": s.Name, "stepId": s.ID, "why": s.Name + " " + s.Status.String()}
		}
	}
	if advance {
		return map[string]any{"on": "nothing", "why": "ready to advance"}
	}
	why := strings.TrimSpace(string(d.State) + ": " + d.StateDetail)
	switch d.State {
	case workflow.ActivityBlocked:
		return map[string]any{"on": "a fix", "why": why}
	case workflow.ActivityWaiting:
		return map[string]any{"on": "a person", "why": why}
	case "":
		return map[string]any{"on": "not known", "why": "the forge did not say what the run is doing"}
	}
	return map[string]any{"on": "the forge", "why": why}
}
func mcpStepOf(s *workflow.Step) mcpStep {
	return mcpStep{ID: s.ID, Stage: s.Stage, Name: s.Name, Type: s.Type, Status: s.Status,
		StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, OverrideBy: s.OverrideBy}
}
func mcpEvidenceSummary(raw string) any {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return strs.Truncate(raw, 500)
	}
	const maxString, maxNested = 600, 4096
	for k, v := range m {
		switch t := v.(type) {
		case string:
			m[k] = strs.Truncate(t, maxString)
		case map[string]any, []any:
			if b, err := json.Marshal(t); err == nil && len(b) > maxNested {
				m[k] = fmt.Sprintf("<%d bytes, see the web page>", len(b))
			}
		}
	}
	return m
}
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 10*1024:
		return fmt.Sprintf("%.1fk", float64(n)/1024)
	default:
		return fmt.Sprintf("%dk", n/1024)
	}
}
func (c *Call) changeOut(ch *workflow.Change, run *workflow.Run) map[string]any {
	out := map[string]any{"id": ch.ID, "title": ch.Title, "state": ch.State, "branch": ch.Branch, "baseSha": ch.BaseSHA,
		"webUrl": c.changeURL(ch.ID)}
	if run != nil {
		out["runId"] = run.ID
		out["workflow"] = run.WorkflowName
	}
	return out
}
func (c *Call) gitURL(p *project.Project) string { return c.BaseURL + "/git/" + p.Slug }
func gitWithTicket(args ...string) []string {
	helper := `!f() { echo username=x-forge-ticket; echo "password=$FORGE_GIT_TICKET"; }; f`
	return append([]string{"git", "-c", "credential.helper=", "-c", "credential.helper=" + helper}, args...)
}
func shellWithTicket(tok string, args ...string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	parts := []string{"FORGE_GIT_TICKET=" + q(tok)}
	for _, a := range gitWithTicket(args...) {
		if a == "<dir>" {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, q(a))
	}
	return strings.Join(parts, " ")
}

type mcpStep struct {
	ID         ids.ID             `json:"id"`
	Stage      string             `json:"stage"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Status     workflow.StepState `json:"status"`
	StartedAt  stamp.Time         `json:"startedAt,omitzero"`
	FinishedAt stamp.Time         `json:"finishedAt,omitzero"`
	OverrideBy string             `json:"overrideBy,omitempty"`
}

func parseJSONMap(raw string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// absent says a read failed only because there is no such record. Any other
// failure is returned as an error, never shown to the model as absence.
func absent(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || fault.KindOf(err) == fault.NotFound
}
func contributionError(err error) error {
	var refusal repositoryedit.ContributionRefusal
	if errors.As(err, &refusal) {
		return refuse("%s", refusal)
	}
	return err
}
