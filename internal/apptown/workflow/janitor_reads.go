package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
}

type janitorChoice struct {
	Level  string       `json:"level"`
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
	Reason string       `json:"reason"`
}

type janitorProcedure struct {
	Title        string `json:"title"`
	Workflow     string `json:"workflow"`
	Version      int    `json:"version"`
	RepairRounds int    `json:"repairRounds"`
	StopRule     string `json:"stopRule"`
	Sentence     string `json:"sentence"`
	Problem      string `json:"problem"`
}

type janitorState struct {
	Word    string `json:"word"`
	Unknown bool   `json:"unknown"`
	Help    string `json:"help"`
}

type JanitorProject struct {
	State          janitorState       `json:"state"`
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
// scan owners used by the project view. A failed scan or eligibility read stays
// unknown on its row; a database failure in queue or consent reads fails the
// read as a whole.
func (s *Service) JanitorProjects(ctx context.Context) ([]JanitorProject, error) {
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
	out := make([]JanitorProject, 0, len(labels))
	for _, p := range labels {
		item := JanitorProject{ProjectID: p.ID, Slug: p.Slug, Name: p.Name, Queues: []QueueStatus{}, Procedures: []janitorProcedure{}, QueueSentences: []string{}}
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
		definitions, err := s.adoptedProcedures(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		item.Queues, err = s.queues(ctx, p.ID, definitions)
		if err != nil {
			return nil, err
		}
		item.Scans, err = s.LatestMainScans(ctx, p.ID)
		if err != nil {
			item.Problem = err.Error()
			item.Scans = MainScans{Reason: err.Error()}
		}
		item.QueueSentences = janitorQueueSentences(item.Queues, item.Enabled && item.Level != "off")
		janitorProcedures(&item, definitions)
		item.State = janitorProjectState(item)
		out = append(out, item)
	}
	return out, nil
}

// These are the only templates for queue policy on the janitor page. The stop
// bound is the one admission uses, and each queue keeps its own failure count.
func janitorQueueSentences(queues []QueueStatus, enabled bool) []string {
	out := []string{}
	var active []string
	for _, q := range queues {
		if enabled && !q.Stopped() {
			active = append(active, q.Title)
		}
	}
	for _, q := range queues {
		sentence := fmt.Sprintf("%s stop after %d failed tries.", q.Title, queueStopAfter)
		if q.Stopped() {
			sentence = q.Title + " " + queueWords(QueueStatus{State: "stopped", Failed: q.Failed}) + "."
			if len(active) > 0 {
				sentence += " " + strings.Join(active, " and ") + " can still run."
			}
		}
		out = append(out, sentence)
	}
	return out
}

type adoptedProcedure struct {
	name    string
	version int
	doc     *schema.Doc
	problem string
}

func (s *Service) adoptedProcedures(ctx context.Context, pid ids.ID) ([]adoptedProcedure, error) {
	receipt, err := adoption.CurrentReceiptIn(ctx, s.db, pid)
	if _, err := database.Lookup(err); err != nil || receipt == nil {
		return nil, err
	}
	if s.janitorDefinitions == nil {
		return nil, fmt.Errorf("procedure observations are unavailable")
	}
	var out []adoptedProcedure
	for _, wf := range receipt.Receipt.Workflows {
		version, body, err := s.janitorDefinitions.WorkflowVersion(ctx, adoption.Source{ProjectID: pid, Revision: receipt}, wf.Name)
		var doc *schema.Doc
		if err == nil {
			doc, err = schema.ParseWorkflowStored([]byte(body))
		}
		item := adoptedProcedure{name: wf.Name, version: version, doc: doc}
		if err != nil {
			item.problem = err.Error()
		}
		out = append(out, item)
	}
	return out, nil
}

func janitorProcedures(p *JanitorProject, definitions []adoptedProcedure) {
	queues := map[string]string{}
	for _, d := range definitions {
		if d.doc == nil {
			p.Problem = d.problem
			continue
		}
		if _, ok := d.doc.Standing(); ok {
			queues[d.name] = d.doc.Metadata.DisplayTitle()
			for _, name := range d.doc.Calls() {
				queues[name] = d.doc.Metadata.DisplayTitle()
			}
		}
	}
	for _, d := range definitions {
		title, ok := queues[d.name]
		if !ok || d.doc == nil || d.doc.RepairRounds() == 0 {
			continue
		}
		rounds := d.doc.RepairRounds()
		p.Procedures = append(p.Procedures, janitorProcedure{Workflow: d.name, Title: d.doc.Metadata.DisplayTitle(), Version: d.version, RepairRounds: rounds,
			StopRule: fmt.Sprintf("%s stop after %d failed tries.", title, queueStopAfter),
			Sentence: fmt.Sprintf("Makes the declared change; if a check fails, up to %d repair sessions; then asks a person.", rounds)})
	}
}

// janitorProjectState classifies the completed read, including procedure problems.
func janitorProjectState(p JanitorProject) janitorState {
	state := janitorState{}
	active, stopped, stuck, waiting := false, -1, -1, false
	for i, q := range p.Queues {
		switch q.State {
		case "updating":
			active = true
		case "stopped":
			if stopped < 0 {
				stopped = i
			}
		case "stuck":
			if stuck < 0 {
				stuck = i
			}
		case "waiting":
			waiting = true
		}
	}
	switch {
	case p.Level == "off":
		state.Word = "turned off for this project"
	case !p.Enabled:
		state.Word = "turned off"
	case p.Scans.NoRepository:
		state.Word = "no repository"
	case stopped >= 0:
		state.Word = p.QueueSentences[stopped]
		if active {
			state.Word += " · " + queueWords(QueueStatus{State: "updating"})
		}
	case active:
		state.Word = queueWords(QueueStatus{State: "updating"})
	case stuck >= 0:
		state.Word = p.Queues[stuck].Words
	case p.Problem != "" || !p.Scans.Known:
		state.Word, state.Unknown, state.Help = "unknown", true, p.Problem
		if state.Help == "" {
			state.Help = p.Scans.Reason
		}
		if state.Help == "" {
			state.Help = "Scan not known"
		}
	case waiting:
		state.Word = queueWords(QueueStatus{State: "waiting"})
	default:
		state.Word = queueWords(QueueStatus{State: "up_to_date"})
	}
	return state
}

// JanitorReaches uses the same ordinary, active project set as the Janitor page.
func (s *Service) JanitorReaches(ctx context.Context, id ids.ID) (bool, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: id}); err != nil {
		return false, err
	}
	labels, err := workflowsql.New(s.db.SQL()).JanitorProjectLabels(ctx, []ids.ID{id})
	return len(labels) != 0, err
}
