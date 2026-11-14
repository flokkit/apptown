package changeactions

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/flokkit/apptown/internal/fault"
)

// Schedules fires workflows with on.schedule for projects whose adopted packs
// explicitly list those workflows under schedules. A firing
// opens a Change and starts its run, attributed to AppTown acting through
// the schedule.
type Schedules struct {
	Actions  *Service
	Adoption *adoption.Service
	Projects *project.Projects
	DB       *database.DB
	Standing *workflow.StandingRules
}

// FireDueSchedules fires each pack-supplied schedule that is due at now, reading
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
	var candidates []firing
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
			candidates = append(candidates, firing{carried: c, projectID: pid, slug: slug})
		}
	}

	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		due := c
		eligible, err := s.resolve(ctx, &due)
		if err != nil {
			log.Printf("schedule eligibility: %v", err)
			continue
		}
		if !eligible {
			continue
		}
		spec, err := schema.ParseCron(due.trigger.Cron)
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
		fired, err := s.fire(ctx, due, now, "")
		if err != nil {
			log.Printf("schedule %s/%s: %v", c.slug, c.workflow, err)
		}
		if fired.outcome == fireStopped || fired.outcome == fireBusy {
			if err := workflow.RecordScheduleFiringIn(ctx, s.DB, c.projectID, c.workflow, now, workflow.ScheduleSkipped, ids.Nil, due.rule.RuleID, due.rule.Revision); err != nil {
				log.Printf("recording the skipped firing of %s: %v", c.workflow, err)
			}
			if fired.outcome == fireBusy && due.trigger.Queue == "" {
				audit.At(now).BestEffort(ctx, s.DB, "apptown-system", c.projectID, "trigger.skipped", c.workflow,
					map[string]any{"reason": "previous run still active"})
			}
		}
	}
}

// firing is one carried schedule of one project, resolved for firing: a
// standing schedule carries its eligible rule and declared cadence.
type firing struct {
	carried
	projectID ids.ID
	slug      string
	waiting   string
	level     string
}

// resolve reads current consent and chooses the adopted cadence into c.
// false: no eligible rule, so the schedule does not fire. A schedule that
// is not standing is always eligible.
func (s Schedules) resolve(ctx context.Context, c *firing) (bool, error) {
	if c.standing == nil {
		return true, nil
	}
	if s.Standing == nil {
		c.waiting = "waits for a standing approval"
		return false, nil
	}
	rule, level, refused, err := s.Standing.StandingForWorkflow(ctx, c.projectID, c.workflow)
	if err != nil {
		return false, err
	}
	if rule.RuleID.IsZero() {
		c.waiting = refused
		return false, nil
	}
	c.rule = rule
	c.level = level
	c.trigger.Cron = c.standing.CronFor(level)
	return true, nil
}

type fireOutcome int

const (
	// fireNone: the firing could not be judged or its change was not opened;
	// the error says why.
	fireNone fireOutcome = iota
	fireOpened
	// fireStopped: the schedule's queue stopped after failed tries and the
	// workflow does not discover new targets.
	fireStopped
	// fireBusy: the queue's change is still working, or a queue-less
	// schedule's previous run is still active.
	fireBusy
)

// fired is what one firing did. Busy names the queue's working change when
// there is one.
type fired struct {
	outcome    fireOutcome
	change     *workflow.Change
	run        *workflow.Run
	busy       ids.ID
	busyNumber int64
}

// fire opens the schedule's change and starts its run now, as AppTown
// acting through the schedule, whatever the last firing time: the tick
// decides when a schedule is due, a request fires it early. A stopped or
// busy queue opens nothing and records nothing; the caller decides what to
// record. An opened or failed firing is recorded. requestedBy is the
// receipt of the person who asked for this firing, "" for the tick. Errors
// are returned, not logged: a failed admission (outcome fireNone), a change
// committed but not prepared or dispatched (the error names it), and a
// firing record that failed after the run started (outcome fireOpened).
func (s Schedules) fire(ctx context.Context, c firing, now time.Time, requestedBy string) (fired, error) {
	admitted, err := s.admit(ctx, c)
	if err != nil || admitted.outcome != fireNone {
		return admitted, err
	}
	title := fmt.Sprintf("%s @ %s", c.workflow, now.Format("2006-01-02 15:04"))
	if err := ctx.Err(); err != nil {
		return fired{}, err
	}
	outcome, changeID := workflow.ScheduleFailed, ids.Nil
	item, run, err := s.Actions.work.CreateAndRun(asSchedule(ctx, c.projectID), workflow.InvokeChange{Change: workflow.ChangeDraft{ProjectID: c.projectID, Type: "scheduled", Title: title, Queue: c.trigger.Queue, Schedule: c.workflow, RuleID: c.rule.RuleID, RuleRevision: c.rule.Revision}, Workflow: c.workflow, Inputs: json.RawMessage(fmt.Sprintf(`{"level":%q}`, c.level))})
	// A change committed before preparation or dispatch failed is kept in the
	// result, so the caller can name it and its requester.
	out := fired{change: item, run: run}
	if err == nil {
		outcome, changeID = workflow.ScheduleOpened, item.ID
		out.outcome = fireOpened
		receipt := map[string]any{"changeId": item.ID, "runId": run.ID, "trigger": "schedule", "cron": c.trigger.Cron, "ruleId": c.rule.RuleID, "ruleRevision": c.rule.Revision}
		if requestedBy != "" {
			receipt["requestedBy"] = requestedBy
		}
		audit.At(now).BestEffort(ctx, s.DB, "apptown-system", c.projectID, "change.triggered", title,
			receipt)
	}
	// The firing is recorded even when the run could not be created:
	// a schedule retries next tick, not every tick.
	if rerr := workflow.RecordScheduleFiringIn(ctx, s.DB, c.projectID, c.workflow, now, outcome, changeID, c.rule.RuleID, c.rule.Revision); rerr != nil {
		rerr = fmt.Errorf("recording the firing of %s: %w", c.workflow, rerr)
		if err != nil {
			return out, errors.Join(err, rerr)
		}
		return out, fmt.Errorf("change %s started; %w", item.ID, rerr)
	}
	return out, err
}

// admit is shared by firing and reads of current operational admission.
func (s Schedules) admit(ctx context.Context, c firing) (fired, error) {
	if c.trigger.Queue != "" {
		state, err := s.Actions.work.QueueState(asSchedule(ctx, c.projectID), c.projectID, c.trigger.Queue)
		if err != nil {
			return fired{}, fmt.Errorf("reading queue %s: %w", c.trigger.Queue, err)
		}
		if state.Stopped() && !c.discoversTargets {
			return fired{outcome: fireStopped}, nil
		}
		if state.State == "updating" {
			return fired{outcome: fireBusy, busy: state.ChangeID, busyNumber: state.ChangeNumber}, nil
		}
	}
	if c.skipIfActive() && c.trigger.Queue == "" {
		active, err := workflow.RunningWorkflowCountIn(ctx, s.DB, c.projectID, c.workflow)
		if err != nil {
			return fired{}, fmt.Errorf("checking for an active run of %s: %w", c.workflow, err)
		}
		if active > 0 {
			return fired{outcome: fireBusy}, nil
		}
	}
	return fired{}, nil
}

func (f fired) refusal(queue string) string {
	switch f.outcome {
	case fireBusy:
		if f.busyNumber > 0 {
			return fmt.Sprintf("Already running: change #%d", f.busyNumber)
		}
		return "Already running"
	case fireStopped:
		return fmt.Sprintf("the %s queue stopped after failed tries", queue)
	}
	return ""
}

// RunNow fires one schedule the project carries now, at the caller's
// request, through the same firing as the tick: the change is AppTown's,
// opened through the schedule under its standing rule, and the request is
// the caller's, recorded under their receipt. The caller must be allowed to
// open work in the project. It fires whenever the schedule last fired and
// arms a schedule the tick has not seen yet; the firing is recorded, so the
// next scheduled firing follows its cron from now. A busy or stopped queue
// is refused and records nothing. now is AppTown's clock.
func (s Schedules) RunNow(ctx context.Context, p project.Project, workflowName string, now time.Time) (*workflow.Change, error) {
	receiptActor, err := s.Actions.work.MayOpenWork(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if !p.Purpose.Schedules() {
		return nil, fault.New(fault.Unprocessable, "an eval copy runs no schedule")
	}
	carried, err := s.carriedBy(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var c *firing
	for _, one := range carried {
		if one.workflow == workflowName {
			c = &firing{carried: one, projectID: p.ID, slug: p.Slug}
		}
	}
	if c == nil {
		return nil, fault.New(fault.NotFound, fmt.Sprintf("the project's packs do not schedule %s", workflowName))
	}
	if c.err != nil {
		return nil, fmt.Errorf("its workflow could not be read: %w", c.err)
	}
	eligible, err := s.resolve(ctx, c)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, fault.New(fault.Unprocessable, c.waiting)
	}
	// A schedule the tick has not seen yet is armed here, so its firing is
	// recorded and the tick follows its cron from now.
	if _, found, err := workflow.ScheduleLastFiredIn(ctx, s.DB, p.ID, workflowName); err != nil {
		return nil, err
	} else if !found {
		if err := workflow.ArmScheduleIn(ctx, s.DB, p.ID, workflowName, now); err != nil {
			return nil, err
		}
	}
	got, err := s.fire(ctx, *c, now, receiptActor)
	if got.change != nil {
		// A change committed, whether or not its run started: the request is
		// recorded though the caller has gone.
		receipt := map[string]any{"changeId": got.change.ID, "ruleId": c.rule.RuleID, "ruleRevision": c.rule.Revision}
		if got.run != nil {
			receipt["runId"] = got.run.ID
		}
		if rerr := audit.At(now).Record(context.WithoutCancel(ctx), s.DB, receiptActor, p.ID, "schedule.run-requested", workflowName, receipt); rerr != nil {
			err = errors.Join(err, fmt.Errorf("change %s started; recording the request: %w", got.change.ID, rerr))
		}
	}
	if err != nil {
		return got.change, err
	}
	if why := got.refusal(c.trigger.Queue); why != "" {
		return nil, fault.New(fault.Conflict, why)
	}
	return got.change, nil
}

// carried is a schedule from a workflow scheduled by an adopted pack, whose
// resolved version declares on.schedule. err says why the
// workflow could not be read, so whether it carries one is unknown.
type carried struct {
	standing         *schema.StandingTrigger
	rule             workflow.StandingRevision
	discoversTargets bool
	workflow         string
	version          int
	trigger          schema.ScheduleTrigger
	err              error
}

func (c carried) skipIfActive() bool {
	return c.trigger.SkipIfActive == nil || *c.trigger.SkipIfActive
}

// carriedBy is every schedule the project's adopted packs supply, the one
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
	names, err := s.Adoption.ScheduledWorkflowNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []carried
	for name := range names {
		version, yaml, err := s.Adoption.WorkflowVersion(ctx, adoption.Source{ProjectID: ad.ProjectID, Revision: ad}, name)
		if err != nil {
			out = append(out, carried{workflow: name, err: err})
			continue
		}
		doc, err := schema.ParseWorkflowStored([]byte(yaml))
		if err != nil {
			// Whether it carries a schedule is unknown, not no: the scheduler
			// cannot fire it, and the read says it could not be read.
			out = append(out, carried{workflow: name, version: version, err: fmt.Errorf("its stored definition does not parse: %w", err)})
			continue
		}
		if doc.Metadata.System || doc.Spec.On.Schedule == nil || doc.Spec.On.Schedule.Cron == "" {
			continue
		}
		discovers := false
		for _, stage := range doc.Spec.Stages {
			for _, step := range stage.Steps {
				if step.Uses == "workflow/call" && step.With["findings"] == "fixable" {
					discovers = true
				}
			}
		}
		one := carried{workflow: name, version: version, trigger: *doc.Spec.On.Schedule, discoversTargets: discovers}
		if trigger, ok := doc.Standing(); ok {
			one.standing = &trigger
		}
		out = append(out, one)
	}
	return out, nil
}

// ProjectSchedules is what a project's schedules will do and last did.
// EvalCopy: the project is an eval's throwaway copy, which runs no
// schedule, so none is listed.
type ProjectSchedules struct {
	EvalCopy  bool              `json:"evalCopy"`
	Schedules []ProjectSchedule `json:"schedules"`
}

// ProjectSchedule is one schedule a project carries, named by its source:
// today always the adopted workflow version that declares it. Cron is read
// in UTC, AppTown's time. Next is when the scheduler will next fire it,
// from its own record and its own cron parser; Due says that time has
// passed and the next tick fires it. Last is the last firing, nil when it
// never fired. Problem says why the schedule could not be read; Next and
// Last are then unknown, not absent. Waiting is a normal pause for consent;
// no next firing is scheduled while it waits.
type ProjectSchedule struct {
	Busy             ids.ID                   `json:"-"`
	BusyNumber       int64                    `json:"-"`
	Queue            string                   `json:"-"`
	Standing         bool                     `json:"-"`
	DiscoversTargets bool                     `json:"-"`
	Refused          string                   `json:"-"`
	Source           scheduleSource           `json:"source"`
	Cron             string                   `json:"cron"`
	TimeZone         string                   `json:"timeZone"`
	SkipIfActive     bool                     `json:"skipIfActive"`
	Next             time.Time                `json:"next,omitzero"`
	Due              bool                     `json:"due"`
	Last             *workflow.ScheduleRecord `json:"last,omitempty"`
	Problem          string                   `json:"problem,omitempty"`
	Waiting          string                   `json:"waiting,omitempty"`
}

type scheduleSource struct {
	Workflow string `json:"workflow"`
	Version  int    `json:"version"`
}

// Of is the project's schedules at now (AppTown's clock): each schedule
// its adopted packs supply, with its next and last firing. A
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
		ps := ProjectSchedule{Queue: c.trigger.Queue, Standing: c.standing != nil, DiscoversTargets: c.discoversTargets, Source: scheduleSource{Workflow: c.workflow, Version: c.version}, TimeZone: "UTC", Cron: c.trigger.Cron, SkipIfActive: c.skipIfActive()}
		if c.err != nil {
			ps.Problem = "its workflow could not be read: " + c.err.Error()
			out.Schedules = append(out.Schedules, ps)
			continue
		}
		firing := firing{carried: c, projectID: projectID, slug: p.Slug}
		eligible, err := s.resolve(ctx, &firing)
		if err != nil {
			ps.Problem = err.Error()
			out.Schedules = append(out.Schedules, ps)
			continue
		}
		ps.Cron = firing.trigger.Cron
		ps.Waiting = firing.waiting
		ps.Refused = firing.waiting
		stopped := false
		if eligible {
			admitted, err := s.admit(ctx, firing)
			if err != nil {
				ps.Problem = err.Error()
				out.Schedules = append(out.Schedules, ps)
				continue
			}
			ps.Refused = admitted.refusal(c.trigger.Queue)
			ps.Busy, ps.BusyNumber = admitted.busy, admitted.busyNumber
			stopped = admitted.outcome == fireStopped
		}
		spec, err := schema.ParseCron(ps.Cron)
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
		if ps.Waiting != "" || stopped {
			out.Schedules = append(out.Schedules, ps)
			continue
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

// JanitorSchedule is the maintenance page's view of scheduler reads.
type JanitorSchedule struct {
	ScanChangeID     ids.ID    `json:"scanChangeId,omitzero"`
	ScanChangeNumber int64     `json:"scanChangeNumber"`
	NextUpdate       time.Time `json:"nextUpdate"`
	NextReason       string    `json:"nextReason"`
	ScanNow          string    `json:"scanNow"`
	ScanWorkflow     string    `json:"scanWorkflow"`
}

// Janitor selects discovery and update roles from the schedules' declarations.
func (p ProjectSchedules) Janitor() JanitorSchedule {
	out := JanitorSchedule{ScanNow: "No scan schedule adopted", NextReason: "No update schedule adopted"}
	var problem string
	for _, schedule := range p.Schedules {
		if schedule.Problem != "" {
			problem = schedule.Problem
			continue
		}
		if !schedule.Standing {
			continue
		}
		if schedule.DiscoversTargets {
			if out.ScanWorkflow == "" {
				out.ScanWorkflow, out.ScanNow = schedule.Source.Workflow, schedule.Refused
				out.ScanChangeID, out.ScanChangeNumber = schedule.Busy, schedule.BusyNumber
			}
		} else if !schedule.Next.IsZero() {
			if out.NextUpdate.IsZero() || schedule.Next.Before(out.NextUpdate) {
				out.NextUpdate = schedule.Next
				out.NextReason = ""
			}
		} else if out.NextUpdate.IsZero() {
			out.NextReason = schedule.Refused
		}
	}
	if problem != "" {
		out.ScanNow = "Not known: " + problem
		out.NextReason = "Update time not known: " + problem
	}
	return out
}
