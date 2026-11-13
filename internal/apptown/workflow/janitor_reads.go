package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// janitorDefinitions reads the project's adopted procedures, never the latest
// stock copy. It has no execution or admission operation.
type janitorDefinitions interface {
	WorkflowVersion(context.Context, adoption.Source, string) (int, string, error)
	ScheduledWorkflowNames(context.Context, ids.ID) (map[string]bool, error)
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

type janitorProject struct {
	ProjectID      ids.ID             `json:"projectId"`
	Slug           string             `json:"slug"`
	Name           string             `json:"name"`
	Enabled        bool               `json:"enabled"`
	Level          string             `json:"level"`
	Choice         *janitorChoice     `json:"choice,omitempty"`
	Issuer         access.Actor       `json:"issuer"`
	GrantedAt      time.Time          `json:"grantedAt"`
	Queues         []QueueStatus      `json:"queues"`
	Scans          MainScans          `json:"scans"`
	Problem        string             `json:"problem"`
	Procedures     []janitorProcedure `json:"procedures"`
	QueueSentences []string           `json:"queueSentences"`
	NextUpdate     time.Time          `json:"nextUpdate"`
	NextReason     string             `json:"nextReason"`
}

// JanitorRules is the read-only home for consent. Managing consent still uses
// Preview, Enable and Disable, with their existing verified-person checks.
func (s StandingRules) JanitorRules(ctx context.Context) ([]StandingRevision, error) {
	if err := s.require(ctx, access.CapProjectRead, access.Resource{Type: "instance"}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return nil, err
	}
	out := []StandingRevision{}
	for _, row := range rows {
		rule, err := readStandingRevision(row)
		if err != nil {
			return nil, err
		}
		if err := func() error {
			if rule.Question.Definition.Scope == "instance" {
				return nil
			}
			return s.readable(ctx, s.db, rule.Question.Definition)
		}(); err != nil {
			if standingRefusal(err) {
				continue
			}
			return nil, err
		}
		// The saved preview describes the grant, not today's portfolio.
		rule.Question.Portfolio = nil
		out = append(out, rule)
	}
	return out, nil
}

func ruleCovers(rule StandingRevision, pid ids.ID) bool {
	if rule.Question.Definition.Scope == "instance" {
		return true
	}
	for _, p := range rule.Question.Definition.Projects {
		if p.ProjectID == pid {
			return true
		}
	}
	return false
}

// JanitorProjects reads all caller-visible projects through the same queue and
// scan owners used by the project view. A failed scan read stays unknown on its
// row; a database failure in queue or consent reads fails the read as a whole.
func (s *Service) JanitorProjects(ctx context.Context) ([]janitorProject, error) {
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return nil, err
	}
	labels, err := workflowsql.New(s.db.SQL()).JanitorProjectLabels(ctx, visible)
	if err != nil {
		return nil, err
	}
	rules, err := s.standing.JanitorRules(ctx)
	if err != nil {
		return nil, err
	}
	choices, err := workflowsql.New(s.db.SQL()).JanitorProjectChoices(ctx, visible)
	if err != nil {
		return nil, err
	}
	byProject := map[ids.ID]janitorChoice{}
	for _, row := range choices {
		var c janitorChoice
		if err := json.Unmarshal(row.RecordJson, &c); err != nil {
			return nil, err
		}
		byProject[row.ProjectID] = c
	}
	out := make([]janitorProject, 0, len(labels))
	for _, p := range labels {
		item := janitorProject{ProjectID: p.ID, Slug: p.Slug, Name: p.Name, Queues: []QueueStatus{}, Procedures: []janitorProcedure{}, QueueSentences: []string{}}
		for _, rule := range rules {
			if rule.Enabled && ruleCovers(rule, p.ID) {
				item.Enabled = true
				if item.Level == "" {
					item.Level = rule.Question.Definition.Categories.Dependencies
					item.Issuer = rule.Issuer
					item.GrantedAt = rule.EnabledAt
				}
			}
		}
		if choice, ok := byProject[p.ID]; ok {
			item.Choice = &choice
			item.Level = choice.Level
		}
		for _, queue := range []string{"security", "updates", "packs"} {
			state, err := queueStateIn(ctx, s.db, p.ID, queue)
			if err != nil {
				return nil, err
			}
			item.Queues = append(item.Queues, state)
		}
		item.Scans, err = s.LatestMainScans(ctx, p.ID)
		if err != nil {
			item.Problem = err.Error()
			item.Scans = MainScans{Reason: err.Error()}
		}
		securityActive := false
		if item.Enabled && item.Level != "off" {
			rule, _, err := s.standing.StandingForWorkflow(ctx, p.ID, "janitor-scan")
			if err != nil {
				return nil, err
			}
			securityActive = !rule.RuleID.IsZero() && rule.Question.Definition.Categories.SecurityFixes
		}
		item.QueueSentences = janitorQueueSentences(item.Queues, item.Level, securityActive)
		if err := s.janitorProcedures(ctx, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// These are the only templates for queue policy on the janitor page. The stop
// bound is the one admission uses, and each queue keeps its own failure count.
func janitorQueueSentences(queues []QueueStatus, level string, securityActive bool) []string {
	names := map[string]string{"security": "Security fixes", "updates": "Weekly updates", "packs": "Pack updates"}
	if level == "aggressive" {
		names["updates"] = "Nightly updates"
	}
	out := []string{}
	for _, q := range queues {
		if q.Queue == "security" {
			securityActive = securityActive && !q.Stopped()
		}
	}
	for _, q := range queues {
		sentence := fmt.Sprintf("%s stop after %d failed tries.", names[q.Queue], queueStopAfter)
		if q.Stopped() {
			sentence = fmt.Sprintf("%s stopped after %d failed tries.", names[q.Queue], q.Failed)
		}
		if q.Queue == "updates" && q.Stopped() && securityActive {
			sentence += " Security fixes can still run."
		}
		out = append(out, sentence)
	}
	return out
}

// Stop rules describe queue admission; current failures stay on the project row.
func janitorStopRule(workflow, level string) string {
	name := map[string]string{"janitor-fix": "Security fixes", "janitor-update": "Weekly updates", "janitor-adopt": "Pack updates"}[workflow]
	if workflow == "janitor-update" && level == "aggressive" {
		name = "Nightly updates"
	}
	sentence := fmt.Sprintf("%s stop after %d failed tries", name, queueStopAfter)
	if workflow == "janitor-update" {
		return sentence + "; security fixes can still run."
	}
	return sentence + "."
}

func repairProcedure(name string, version int, doc *schema.Doc) janitorProcedure {
	out := janitorProcedure{Workflow: name, Version: version}
	for _, stage := range doc.Spec.Stages {
		for _, step := range stage.Steps {
			if step.Uses == "agent/pi" {
				raw, err := json.Marshal(step.With)
				if err != nil {
					out.Problem = err.Error()
					return out
				}
				var cfg struct {
					RepairRounds int `json:"repairRounds"`
				}
				if err := json.Unmarshal(raw, &cfg); err != nil {
					out.Problem = err.Error()
					return out
				}
				out.RepairRounds += cfg.RepairRounds
			}
		}
	}
	out.Sentence = fmt.Sprintf("Makes the declared change; if a check fails, up to %d repair sessions; then asks a person.", out.RepairRounds)
	return out
}

func (s *Service) janitorProcedures(ctx context.Context, p *janitorProject) error {
	receipt, err := adoption.CurrentReceiptIn(ctx, s.db, p.ProjectID)
	if _, err := database.Lookup(err); err != nil {
		return err
	}
	if receipt == nil {
		p.NextReason = "Needs a pack upgrade"
		return nil
	}
	if s.janitorDefinitions == nil {
		return fmt.Errorf("janitor procedure observations are unavailable")
	}
	source := adoption.Source{ProjectID: p.ProjectID, Revision: receipt}
	names, err := s.janitorDefinitions.ScheduledWorkflowNames(ctx, p.ProjectID)
	if err != nil {
		return err
	}
	records, err := ScheduleRecordsIn(ctx, s.db, p.ProjectID)
	if err != nil {
		return err
	}
	for _, name := range []string{"janitor-fix", "janitor-update", "janitor-adopt"} {
		// An unadopted procedure is absent, not the latest stock procedure.
		pinned := false
		for _, wf := range receipt.Receipt.Workflows {
			if wf.Name == name {
				pinned = true
			}
		}
		if !pinned {
			continue
		}
		version, body, err := s.janitorDefinitions.WorkflowVersion(ctx, source, name)
		proc := janitorProcedure{Workflow: name, Version: version, StopRule: janitorStopRule(name, p.Level)}
		if err != nil {
			proc.Problem = err.Error()
			p.Procedures = append(p.Procedures, proc)
			continue
		}
		doc, err := schema.ParseWorkflowStored([]byte(body))
		if err != nil {
			proc.Problem = err.Error()
			p.Procedures = append(p.Procedures, proc)
			continue
		}
		proc = repairProcedure(name, version, doc)
		proc.StopRule = janitorStopRule(name, p.Level)
		p.Procedures = append(p.Procedures, proc)
		if name != "janitor-update" || !names[name] || doc.Spec.On.Schedule == nil {
			continue
		}
		if !p.Enabled || p.Level == "off" {
			p.NextReason = "Turned off"
			continue
		}
		for _, q := range p.Queues {
			if q.Queue == "updates" && q.Stopped() {
				p.NextReason = "Updates stopped"
			}
		}
		if p.NextReason != "" {
			continue
		}
		cron := doc.Spec.On.Schedule.Cron
		if doc.Spec.On.Schedule.Standing {
			rule, selected, err := s.standing.StandingForWorkflow(ctx, p.ProjectID, name)
			if err != nil {
				return err
			}
			if rule.RuleID.IsZero() {
				p.NextReason = "Waiting for approval or a pack upgrade"
				continue
			}
			cron = selected
		}
		spec, err := schema.ParseCron(cron)
		if err != nil {
			p.NextReason = "Update time not known: " + err.Error()
			continue
		}
		from := s.now().UTC()
		if rec, ok := records[name]; ok {
			from = rec.At
		}
		p.NextUpdate = spec.Next(from)
	}
	if p.NextUpdate.IsZero() && p.NextReason == "" {
		p.NextReason = "No update schedule adopted"
	}
	return nil
}
