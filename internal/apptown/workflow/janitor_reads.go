package workflow

import (
	"context"
	"encoding/json"
	"fmt"

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
	ScheduledWorkflowNames(context.Context, ids.ID) (map[string]bool, error)
	AdoptedWorkflows(context.Context, adoption.Source, ...string) ([]adoption.AdoptedWorkflow, error)
}

type janitorChoice struct {
	Level  string       `json:"level"`
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
	Reason string       `json:"reason"`
}

type janitorState struct {
	Word    string `json:"word"`
	Unknown bool   `json:"unknown"`
	Help    string `json:"help"`
}

// ProcedureFacts is the adoption read shared by queue, scan and schedule reads.
// It carries observations only, never authority for a later command.
type ProcedureFacts struct {
	Scheduled map[string]bool
	Receipt   *adoption.RecordedRevision
	Workflows []adoption.AdoptedWorkflow
}

type JanitorProject struct {
	Eligibility map[string]StandingEligibilityResult `json:"-"`
	Facts       ProcedureFacts                       `json:"-"`
	State       janitorState                         `json:"state"`
	ProjectID   ids.ID                               `json:"projectId"`
	Slug        string                               `json:"slug"`
	Name        string                               `json:"name"`
	Enabled     bool                                 `json:"-"`
	Level       string                               `json:"level"`
	Choice      *janitorChoice                       `json:"choice,omitempty"`
	Queues      []QueueStatus                        `json:"queues"`
	Problem     string                               `json:"-"`
}

// JanitorProjects reads all caller-visible projects through the same queue and
// scan owners used by the project view. A failed scan or eligibility read stays
// unknown on its row; a database failure in queue or consent reads fails the
// read as a whole. Changed and unknown rule IDs retain both observations when
// different projects give different answers.
func (s *Service) JanitorProjects(ctx context.Context) ([]JanitorProject, *StandingRevision, []ids.ID, error) {
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return nil, nil, nil, err
	}
	labels, err := workflowsql.New(s.db.SQL()).JanitorProjectLabels(ctx, visible)
	if err != nil {
		return nil, nil, nil, err
	}
	rule, err := s.standing.janitorChoices(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	unknown := []ids.ID{}
	seenUnknown := map[ids.ID]bool{}
	choices, err := workflowsql.New(s.db.SQL()).JanitorProjectChoices(ctx, visible)
	if err != nil {
		return nil, nil, nil, err
	}
	byProject := map[ids.ID]janitorChoice{}
	for _, row := range choices {
		var c janitorChoice
		if err := json.Unmarshal(row.RecordJson, &c); err != nil {
			return nil, nil, nil, err
		}
		byProject[row.ProjectID] = c
	}
	factsByProject := map[ids.ID]ProcedureFacts{}
	projects := make([]JanitorProject, 0, len(labels))
	for _, p := range labels {
		facts, err := s.procedureFacts(ctx, p.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		factsByProject[p.ID] = facts
		projects = append(projects, JanitorProject{ProjectID: p.ID, Facts: facts})
	}
	// Eligibility failures stay attached to each workflow, like scan failures.
	eligibility, _ := s.janitorEligibility(ctx, projects, rule)
	out := make([]JanitorProject, 0, len(labels))
	for _, p := range labels {
		item := JanitorProject{ProjectID: p.ID, Slug: p.Slug, Name: p.Name, Queues: []QueueStatus{}}
		if rule != nil {
			item.Enabled = rule.Enabled
			item.Level = rule.Choices.Dependencies
		}
		if choice, ok := byProject[p.ID]; ok {
			item.Choice = &choice
			item.Level = choice.Level
		}
		facts := factsByProject[p.ID]
		definitions := parseProcedures(facts)
		item.Queues, err = s.queues(ctx, p.ID, definitions)
		if err != nil {
			return nil, nil, nil, err
		}
		item.Facts = facts
		item.Eligibility = eligibility[p.ID]
		for _, result := range item.Eligibility {
			if result.Err != nil {
				item.Problem = result.Err.Error()
			}
			for _, id := range result.Unassessed {
				if !seenUnknown[id] {
					unknown = append(unknown, id)
					seenUnknown[id] = true
				}
			}

		}
		scans, err := s.latestMainScans(ctx, p.ID, 0, func() (bool, error) { return s.scanSchedules.ScansSourceFrom(ctx, p.ID, facts) })
		if err != nil {
			item.Problem = err.Error()
			scans = MainScans{Reason: err.Error()}
		}
		item.State = janitorProjectState(item, scans)
		out = append(out, item)
	}
	return out, rule, unknown, nil
}

type adoptedProcedure struct {
	name    string
	doc     *schema.Doc
	problem string
}

func (s *Service) procedureFacts(ctx context.Context, pid ids.ID) (ProcedureFacts, error) {
	receipt, err := adoption.CurrentReceiptIn(ctx, s.db, pid)
	if _, err := database.Lookup(err); err != nil || receipt == nil {
		return ProcedureFacts{}, err
	}
	if s.janitorDefinitions == nil {
		return ProcedureFacts{}, fmt.Errorf("procedure observations are unavailable")
	}
	scheduled, err := s.janitorDefinitions.ScheduledWorkflowNames(ctx, pid)
	if err != nil {
		return ProcedureFacts{}, err
	}
	items, err := s.janitorDefinitions.AdoptedWorkflows(ctx, adoption.Source{ProjectID: pid, Revision: receipt})
	return ProcedureFacts{Receipt: receipt, Workflows: items, Scheduled: scheduled}, err
}

func (s *Service) adoptedProcedures(ctx context.Context, pid ids.ID) ([]adoptedProcedure, error) {
	facts, err := s.procedureFacts(ctx, pid)
	return parseProcedures(facts), err
}

func parseProcedures(facts ProcedureFacts) []adoptedProcedure {
	var out []adoptedProcedure
	for _, wf := range facts.Workflows {
		var doc *schema.Doc
		err := wf.Err
		if err == nil {
			doc, err = schema.ParseWorkflowStored([]byte(wf.YAML))
		}
		item := adoptedProcedure{name: wf.Name, doc: doc}
		if err != nil {
			item.problem = err.Error()
		}
		out = append(out, item)
	}
	return out
}

// janitorProjectState classifies the completed read, including procedure problems.
func janitorProjectState(p JanitorProject, scans MainScans) janitorState {
	state := janitorState{}
	active, attention := false, false
	for _, q := range p.Queues {
		switch q.State {
		case "updating", "landing":
			active = true
		case "stopped", "decide":
			attention = true
		}
	}
	switch {
	case p.Choice != nil && p.Choice.Level == "off" || !p.Enabled:
		state.Word = "off"
	case scans.NoRepository:
		state.Word = "no repository"
	case attention:
		state.Word = "needs you"
	case active:
		state.Word = "working"
	case p.Problem != "" || !scans.Known:
		state.Word, state.Unknown, state.Help = "not known", true, p.Problem
		if state.Help == "" {
			state.Help = scans.Reason
		}
		if state.Help == "" {
			state.Help = "Scan not known"
		}
	default:
		state.Word = "up to date"
	}
	return state
}

// ScanAttentionProject contains project-read facts for one Attention source.
// Err leaves this project's source unknown without losing other observations.
type ScanAttentionProject struct {
	ProjectID  ids.ID
	Name, Slug string
	Reaches    bool
	Scans      MainScans
	Queues     []QueueStatus
	Err        error
}

// ScanAttention intersects requested IDs with its own project-read decision.
// Deployment visibility supplied by another owner is never project authority.
func (s *Service) ScanAttention(ctx context.Context, requested []ids.ID) ([]ScanAttentionProject, error) {
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return nil, err
	}
	allowed := map[ids.ID]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	selected := []ids.ID{}
	for _, id := range requested {
		if allowed[id] {
			selected = append(selected, id)
			delete(allowed, id)
		}
	}
	labels, err := workflowsql.New(s.db.SQL()).JanitorProjectLabels(ctx, selected)
	if err != nil {
		out := make([]ScanAttentionProject, 0, len(selected))
		for _, id := range selected {
			out = append(out, ScanAttentionProject{ProjectID: id, Err: err})
		}
		return out, nil
	}
	out := []ScanAttentionProject{}
	for _, id := range selected {
		item := ScanAttentionProject{ProjectID: id}
		for _, p := range labels {
			if p.ID == id {
				item.Reaches = true
				item.Name = p.Name
				item.Slug = p.Slug
				break
			}
		}
		if item.Reaches {
			facts, err := s.procedureFacts(ctx, id)
			definitions := parseProcedures(facts)
			if err == nil {
				for _, d := range definitions {
					if d.problem != "" {
						err = fmt.Errorf("workflow %q: %s", d.name, d.problem)
						break
					}
				}
			}
			if err == nil {
				item.Queues, err = s.queues(ctx, id, definitions)
			}
			if err == nil {
				item.Scans, err = s.latestMainScans(ctx, id, 0, func() (bool, error) { return s.scanSchedules.ScansSourceFrom(ctx, id, facts) })
			}
			item.Err = err
		}
		out = append(out, item)
	}
	return out, nil
}
