package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// JanitorProcedure contains only the adopted facts consumed by schedule views.
// Reading these facts grants no authority to start work.
type JanitorProcedure struct {
	Name                                                  string
	Title                                                 string
	Version                                               int
	Scheduled, Publication, DiscoversTargets, ScansSource bool
	Trigger                                               schema.ScheduleTrigger
	Standing                                              *schema.StandingTrigger
	Problem                                               string
}

// DescribeJanitorProcedure selects the schedule facts from an adopted definition.
func DescribeJanitorProcedure(wf adoption.AdoptedWorkflow, scheduled bool) (JanitorProcedure, *QueueStatus) {
	p := JanitorProcedure{Name: wf.Name, Version: wf.Version, Scheduled: scheduled}
	if wf.Err != nil {
		p.Problem = wf.Err.Error()
		return p, nil
	}
	doc, err := schema.ParseWorkflowStored([]byte(wf.YAML))
	if err != nil {
		p.Problem = err.Error()
		return p, nil
	}
	p.Title = doc.Metadata.DisplayTitle()
	trigger, standing := doc.Standing()
	var queue *QueueStatus
	if standing && trigger.Queue != "" {
		queue = &QueueStatus{Queue: trigger.Queue, Title: doc.Metadata.DisplayTitle(), Workflow: wf.Name}
	}
	if doc.Metadata.System {
		p.Scheduled = false
		return p, queue
	}
	if standing {
		p.Standing = &trigger
		p.Publication = doc.Spec.On.Publication != nil && trigger.Queue != ""
	}
	p.Scheduled = scheduled && doc.Spec.On.Schedule != nil && doc.Spec.On.Schedule.Cron != ""
	if p.Scheduled {
		p.Trigger = *doc.Spec.On.Schedule
		for _, stage := range doc.Spec.Stages {
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				if step.Uses == "scan" && step.With["on"] == "source" {
					p.ScansSource = true
				}
				if step.Uses == "workflow/call" && step.With["findings"] == "fixable" {
					p.DiscoversTargets = true
				}
			}
		}
	}
	return p, queue
}

type scanConclusion struct {
	Known, Failed, Stale bool
	LastScan             time.Time
	Reason               string
}
type janitorScans struct {
	Summary    scanConclusion
	Procedures []JanitorProcedure
	Scheduled  bool
}

func (s janitorScans) summary(now time.Time) MainScans {
	c := s.Summary
	if !c.LastScan.IsZero() && now.Sub(c.LastScan) > 3*24*time.Hour {
		c.Known = false
		c.Stale = true
		c.Reason = "the last scan is more than three days old"
	}
	return MainScans{Known: c.Known, Failed: c.Failed, Stale: c.Stale, Unscheduled: !s.Scheduled, LastScan: c.LastScan, Reason: c.Reason}
}

type janitorUpdate struct {
	queue             string
	service           *Service
	procedures, scans bool
	at                time.Time
}

// recordJanitorIn is the only writer. Every derivation follows the project
// lock, sees this transaction's rows, and writes only the part it derived.
func recordJanitorIn(ctx context.Context, tx *database.Tx, project ids.ID, what janitorUpdate) error {
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, project); err != nil {
		return err
	}
	rows, err := q.JanitorRecord(ctx, project)
	if err != nil {
		return err
	}
	queues := []QueueStatus{}
	scans := janitorScans{}
	if len(rows) > 0 {
		if err := json.Unmarshal(rows[0].Queues, &queues); err != nil {
			return err
		}
		if err := json.Unmarshal(rows[0].Scans, &scans); err != nil {
			return err
		}
	}
	old := map[string]QueueStatus{}
	for _, queue := range queues {
		old[queue.Queue] = queue
	}
	problem := ""
	if what.procedures {
		receipt, err := adoption.CurrentReceiptIn(ctx, tx, project)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var workflows []adoption.AdoptedWorkflow
		scheduled := map[string]bool{}
		if receipt != nil {
			scheduled, err = what.service.janitorDefinitions.ScheduledWorkflowNamesIn(ctx, tx, project)
			if err != nil {
				return err
			}
			workflows, err = what.service.janitorDefinitions.AdoptedWorkflows(ctx, adoption.Source{ProjectID: project, Revision: receipt})
			if err != nil {
				return err
			}
		}
		scans.Procedures = nil
		scans.Scheduled = false
		queues = []QueueStatus{}
		seen := map[string]bool{}
		for _, wf := range workflows {
			p, queue := DescribeJanitorProcedure(wf, scheduled[wf.Name])
			if p.Problem != "" {
				problem = fmt.Sprintf("workflow %q: %s", p.Name, p.Problem)
			}
			if p.Scheduled || p.Publication || p.Problem != "" {
				scans.Procedures = append(scans.Procedures, p)
			}
			scans.Scheduled = scans.Scheduled || p.ScansSource
			if queue != nil && !seen[queue.Queue] {
				seen[queue.Queue] = true
				queues = append(queues, *queue)
			}
		}

		raw, err := json.Marshal(map[string]any{"Procedures": scans.Procedures, "Scheduled": scans.Scheduled})
		if err != nil {
			return err
		}
		if err := q.RecordJanitorProcedures(ctx, workflowsql.RecordJanitorProceduresParams{ProjectID: project, Scans: raw, Problem: problem, UpdatedAt: what.at}); err != nil {
			return err
		}
	}
	if what.queue != "" || what.procedures {
		for i, prior := range queues {
			if !what.procedures && prior.Queue != what.queue {
				continue
			}
			next, err := queueStateIn(ctx, tx, project, prior.Queue)
			if err != nil {
				return err
			}
			next.Title, next.Workflow = prior.Title, prior.Workflow
			next.StateSince = what.at
			if previous, ok := old[prior.Queue]; ok && previous.State == next.State {
				next.StateSince = previous.StateSince
			}
			queues[i] = next
		}
		raw, err := json.Marshal(queues)
		if err != nil {
			return err
		}
		if err := q.RecordJanitorQueues(ctx, workflowsql.RecordJanitorQueuesParams{ProjectID: project, Queues: raw, UpdatedAt: what.at}); err != nil {
			return err
		}
	}
	if what.scans {
		result, err := what.service.latestMainScansIn(ctx, tx, project, 0, func() (bool, error) { return scans.Scheduled, nil })
		if err != nil {
			result = MainScans{Reason: err.Error()}
		}
		if result.NoRepository {
			result = MainScans{}
		}
		raw, err := json.Marshal(map[string]any{"Summary": scanConclusion{result.Known, result.Failed, result.Stale, result.LastScan, result.Reason}})
		if err != nil {
			return err
		}
		return q.RecordJanitorScans(ctx, workflowsql.RecordJanitorScansParams{ProjectID: project, Scans: raw, UpdatedAt: what.at})
	}
	return nil
}

func recordJanitorChangeIn(ctx context.Context, tx *database.Tx, change ids.ID, at time.Time) error {
	attempts, err := workflowsql.New(tx.SQL()).QueueAttempt(ctx, change)
	if err != nil {
		return err
	}
	if len(attempts) == 0 || attempts[0].Queue == "" {
		return nil
	}
	a := attempts[0]
	return recordJanitorIn(ctx, tx, a.ProjectID, janitorUpdate{queue: a.Queue, at: at})
}
func recordJanitorRunIn(ctx context.Context, tx *database.Tx, run ids.ID, at time.Time) error {
	r, err := workflowsql.New(tx.SQL()).JanitorRunChange(ctx, run)
	if err != nil {
		return err
	}
	return recordJanitorChangeIn(ctx, tx, r.ChangeID, at)
}

// RecordJanitorAdoptionIn joins adoption's after-advance transaction.
func (s *Service) RecordJanitorAdoptionIn(ctx context.Context, tx *database.Tx, project ids.ID, at time.Time) error {
	return recordJanitorIn(ctx, tx, project, janitorUpdate{service: s, procedures: true, at: at})
}

// RecordJanitorScansIn records a conclusion after the attempt's observations.
func (e *Execution) RecordJanitorScansIn(ctx context.Context, tx *database.Tx) error {
	return recordJanitorIn(ctx, tx, e.run.ProjectID, janitorUpdate{service: e.service, scans: true, at: e.now()})
}

// RecordJanitorProjects repairs conclusions once after startup interruption.
func (s *Service) RecordJanitorProjects(ctx context.Context) error {
	projects, err := workflowsql.New(s.db.SQL()).JanitorProjectIDs(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err := s.db.InTx(ctx, func(tx *database.Tx) error {
			return recordJanitorIn(ctx, tx, project, janitorUpdate{service: s, procedures: true, scans: true, at: s.now()})
		}); err != nil {
			return err
		}
	}
	return nil
}
