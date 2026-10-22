package changeactions

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Schedules fires scheduled workflows: library workflows with on.schedule,
// for every project that selected them (design/decisions/workflows-as-lanes.md:
// the schedule is a trigger on an explicitly selected, pinned lane). A firing
// opens a Change and starts its run, attributed to AppTown acting through
// the schedule.
type Schedules struct {
	Actions  *Service
	Adoption *adoption.Service
	Projects *project.Projects
	DB       *database.DB
}

// FireDueSchedules fires each selected schedule that is due at now, reading
// the cron from the workflow version the project adopted. A schedule seen
// for the first time is armed at now, and missed historical firings are not
// replayed. A schedule whose previous run is still active skips the firing
// unless it says skipIfActive: false; the skip is recorded. A firing is
// recorded even when its run could not be created, so a schedule retries
// next tick, not every tick. Nobody waits on a tick: a failed read skips
// that project or schedule, said in the log.
func (s Schedules) FireDueSchedules(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	type sched struct {
		carried
		projectID ids.ID
		slug      string
	}
	var candidates []sched
	projects := map[ids.ID]string{}
	all, err := s.Projects.All(ctx)
	if err != nil {
		log.Printf("scheduler: reading projects: %v", err)
		return
	}
	for _, p := range all {
		// An eval's throwaway copy runs only what its eval starts in it.
		if !p.Purpose.Schedules() {
			continue
		}
		projects[p.ID] = p.Slug
	}
	for pid, slug := range projects {
		carried, err := s.carriedBy(ctx, pid)
		if err != nil {
			log.Printf("read failed: %v", err)
			continue
		}
		for _, c := range carried {
			if c.err != nil {
				log.Printf("schedule %s/%s: %v", slug, c.workflow, c.err)
				continue
			}
			candidates = append(candidates, sched{carried: c, projectID: pid, slug: slug})
		}
	}

	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		spec, err := schema.ParseCron(c.trigger.Cron)
		if err != nil {
			continue
		}
		last, found, err := workflow.ScheduleLastFiredIn(ctx, s.DB, c.projectID, c.workflow)
		if err != nil {
			log.Printf("schedule %s/%s: reading last firing: %v", c.slug, c.workflow, err)
			continue
		}
		if !found {
			// A new schedule starts now; missed historical firings are not replayed.
			if err := workflow.ArmScheduleIn(ctx, s.DB, c.projectID, c.workflow, now); err != nil {
				log.Printf("arming the schedule for %s: %v", c.workflow, err)
			}
			continue
		}
		if spec.Next(last.Time).After(now) {
			continue
		}

		if c.skipIfActive() {
			active, err := workflow.RunningWorkflowCountIn(ctx, s.DB, c.projectID, c.workflow)
			if err != nil {
				log.Printf("checking scheduled workflow %s on project %d: %v", c.workflow, c.projectID, err)
				continue
			}
			if active > 0 {
				if err := workflow.RecordScheduleFiringIn(ctx, s.DB, c.projectID, c.workflow, now, workflow.ScheduleSkipped, ids.Nil); err != nil {
					log.Printf("recording the skipped firing of %s: %v", c.workflow, err)
				}
				audit.At(now).BestEffort(ctx, s.DB, "apptown-system", c.projectID, "trigger.skipped", c.workflow,
					map[string]any{"reason": "previous run still active"})
				continue
			}
		}

		p := &project.Project{ID: c.projectID, Slug: c.slug}
		title := fmt.Sprintf("%s @ %s", c.workflow, now.Format("2006-01-02 15:04"))
		if ctx.Err() != nil {
			return
		}
		outcome, changeID := workflow.ScheduleFailed, ids.Nil
		item, run, err := s.Actions.CreateAndStart(asSchedule(ctx, c.projectID), p, "scheduled", title, "", c.workflow)
		if err == nil {
			outcome, changeID = workflow.ScheduleOpened, item.ID
			audit.At(now).BestEffort(ctx, s.DB, "apptown-system", c.projectID, "change.triggered", title,
				map[string]any{"changeId": item.ID, "runId": run.ID, "trigger": "schedule", "cron": c.trigger.Cron})
		}
		// The firing is recorded even when the run could not be created:
		// a schedule retries next tick, not every tick.
		if err := workflow.RecordScheduleFiringIn(ctx, s.DB, c.projectID, c.workflow, now, outcome, changeID); err != nil {
			log.Printf("recording the firing of %s: %v", c.workflow, err)
		}
	}
}

// carried is a schedule a project carries: a workflow it adopted and
// selected whose adopted version declares on.schedule. err says why the
// workflow could not be read, so whether it carries one is unknown.
type carried struct {
	workflow string
	version  int
	trigger  schema.ScheduleTrigger
	err      error
}

func (c carried) skipIfActive() bool {
	return c.trigger.SkipIfActive == nil || *c.trigger.SkipIfActive
}

// carriedBy is every schedule the project's adopted workflows carry, the one
// enumeration the scheduler fires from and the project's read shows, so the
// two cannot disagree about which schedules exist. A project with nothing
// adopted carries none; a failed adoption read is an error.
func (s Schedules) carriedBy(ctx context.Context, projectID ids.ID) ([]carried, error) {
	ad, err := adoption.CurrentReceiptIn(ctx, s.DB, projectID)
	if _, err := database.Lookup(err); err != nil {
		return nil, err
	}
	if ad == nil {
		return nil, nil
	}
	var out []carried
	for _, wf := range ad.Receipt.Workflows {
		selected, err := s.Adoption.LaneSelected(ctx, projectID, wf.Name)
		if err != nil {
			out = append(out, carried{workflow: wf.Name, err: fmt.Errorf("reading the project's lanes: %w", err)})
			continue
		}
		if !selected {
			continue
		}
		version, yaml, err := s.Adoption.WorkflowVersion(ctx, adoption.Source{ProjectID: ad.ProjectID, Revision: ad}, wf.Name)
		if err != nil {
			out = append(out, carried{workflow: wf.Name, err: err})
			continue
		}
		doc, err := schema.ParseWorkflowStored([]byte(yaml))
		if err != nil {
			// Whether it carries a schedule is unknown, not no: the scheduler
			// cannot fire it, and the read says it could not be read.
			out = append(out, carried{workflow: wf.Name, version: version, err: fmt.Errorf("its stored definition does not parse: %w", err)})
			continue
		}
		if doc.Spec.On.Schedule == nil || doc.Spec.On.Schedule.Cron == "" {
			continue
		}
		out = append(out, carried{workflow: wf.Name, version: version, trigger: *doc.Spec.On.Schedule})
	}
	return out, nil
}

// ProjectSchedules is what a project's schedules will do and last did.
// EvalCopy: the project is an eval's throwaway copy, which runs no
// schedule, so none is listed.
type ProjectSchedules struct {
	EvalCopy  bool
	Schedules []ProjectSchedule
}

// ProjectSchedule is one schedule a project carries, named by its source:
// today always the adopted workflow version that declares it. Cron is read
// in UTC, AppTown's time. Next is when the scheduler will next fire it,
// from its own record and its own cron parser; Due says that time has
// passed and the next tick fires it. Last is the last firing, nil when it
// never fired. Problem says why the schedule could not be read; Next and
// Last are then unknown, not absent.
type ProjectSchedule struct {
	Workflow     string
	Version      int
	Cron         string
	SkipIfActive bool
	Next         time.Time
	Due          bool
	Last         *workflow.ScheduleRecord
	Problem      string
}

// Of is the project's schedules at now (AppTown's clock): each schedule
// its adopted, selected workflows carry, with its next and last firing. A
// schedule not yet armed is armed at the scheduler's next tick and fires
// first at its next time after that. Missed firings are never replayed, so
// a past Next means one firing at the next tick, not a backlog.
func (s Schedules) Of(ctx context.Context, p project.Project, now time.Time) (ProjectSchedules, error) {
	// An eval's throwaway copy runs only what its eval starts in it.
	if !p.Purpose.Schedules() {
		return ProjectSchedules{EvalCopy: true, Schedules: []ProjectSchedule{}}, nil
	}
	projectID := p.ID
	carried, err := s.carriedBy(ctx, projectID)
	if err != nil {
		return ProjectSchedules{}, err
	}
	records, err := workflow.ScheduleRecordsIn(ctx, s.DB, projectID)
	if err != nil {
		return ProjectSchedules{}, err
	}
	out := ProjectSchedules{Schedules: []ProjectSchedule{}}
	for _, c := range carried {
		ps := ProjectSchedule{Workflow: c.workflow, Version: c.version, Cron: c.trigger.Cron, SkipIfActive: c.skipIfActive()}
		if c.err != nil {
			ps.Problem = "its workflow could not be read: " + c.err.Error()
			out.Schedules = append(out.Schedules, ps)
			continue
		}
		spec, err := schema.ParseCron(c.trigger.Cron)
		if err != nil {
			ps.Problem = "its cron is not understood, so it never fires: " + err.Error()
			out.Schedules = append(out.Schedules, ps)
			continue
		}
		// The scheduler reads its cron in UTC: it judges each firing from
		// the time it recorded, which the database hands back in UTC.
		from := now.UTC()
		if rec, ok := records[c.workflow]; ok {
			from = rec.At
			if rec.Outcome != workflow.ScheduleArmed {
				last := rec
				ps.Last = &last
			}
		}
		ps.Next = spec.Next(from)
		ps.Due = !ps.Next.After(now)
		out.Schedules = append(out.Schedules, ps)
	}
	return out, nil
}

// asSchedule is AppTown acting for itself in a project, through a schedule.
func asSchedule(ctx context.Context, projectID ids.ID) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: projectID}, Verified: true, Receipt: "system"})
}
