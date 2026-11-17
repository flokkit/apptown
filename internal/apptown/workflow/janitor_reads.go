package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// janitorDefinitions reads the project's adopted procedures, never the latest
// stock copy. It has no execution or admission operation.
type janitorDefinitions interface {
	ScheduledWorkflowNamesIn(context.Context, database.Queryer, ids.ID) (map[string]bool, error)
	AdoptedWorkflows(context.Context, adoption.Source, ...string) ([]adoption.AdoptedWorkflow, error)
}

type janitorChoice struct {
	Level  string       `json:"level"`
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
}

type janitorState struct {
	Word    string `json:"word"`
	Unknown bool   `json:"unknown"`
	Help    string `json:"help"`
}

type JanitorProject struct {
	Eligibility map[string]StandingEligibilityResult `json:"-"`
	Scans       MainScans                            `json:"-"`
	Procedures  []JanitorProcedure                   `json:"-"`
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
	labels, err := workflowsql.New(s.db.SQL()).JanitorRecords(ctx, visible)
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
	byProject := map[ids.ID]*standingProjectAct{}
	for _, row := range choices {
		c, err := projectChoiceFrom(row.RecordJson)
		if err != nil {
			return nil, nil, nil, err
		}
		byProject[row.ProjectID] = c
	}
	projects := make([]JanitorProject, 0, len(labels))
	for _, p := range labels {
		item := JanitorProject{ProjectID: p.ID, Queues: []QueueStatus{}, Problem: p.Problem}
		var scans janitorScans
		if err := json.Unmarshal(p.Queues, &item.Queues); err != nil {
			return nil, nil, nil, err
		}
		if err := json.Unmarshal(p.Scans, &scans); err != nil {
			return nil, nil, nil, err
		}
		item.Procedures = scans.Procedures
		item.Scans = scans.summary(s.now())
		item.Scans.NoRepository = p.DefaultBranch == ""
		projects = append(projects, item)
	}
	// Eligibility failures stay attached to each workflow, like scan failures.
	eligibility, _ := s.janitorEligibility(ctx, projects, rule)
	out := make([]JanitorProject, 0, len(labels))
	for i, p := range labels {
		item := projects[i]
		item.Slug, item.Name = p.Slug, p.Name
		var in JanitorChoices
		if rule != nil {
			item.Enabled = rule.Enabled
			in = rule.Choices
		}
		choice := byProject[p.ID]
		item.Level = projectLevel(in, choiceLevel(choice))
		if choice != nil {
			item.Choice = &janitorChoice{Level: choice.Level, Person: choice.Person, At: choice.At}
		}
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
		item.State = janitorProjectState(item, item.Scans)
		out = append(out, item)
	}
	return out, rule, unknown, nil
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
	rows, err := workflowsql.New(s.db.SQL()).JanitorRecords(ctx, selected)
	if err != nil {
		out := make([]ScanAttentionProject, 0, len(selected))
		for _, id := range selected {
			out = append(out, ScanAttentionProject{ProjectID: id, Err: err})
		}
		return out, nil
	}
	out := make([]ScanAttentionProject, 0, len(rows))
	for _, r := range rows {
		item := ScanAttentionProject{ProjectID: r.ID, Name: r.Name, Slug: r.Slug, Reaches: true, Queues: []QueueStatus{}}
		var scans janitorScans
		if err := json.Unmarshal(r.Queues, &item.Queues); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(r.Scans, &scans); err != nil {
			return nil, err
		}
		item.Scans = scans.summary(s.now())
		item.Scans.NoRepository = r.DefaultBranch == ""
		if r.Problem != "" {
			item.Err = fmt.Errorf("%s", r.Problem)
		}
		out = append(out, item)
	}
	return out, nil
}
