package changeactions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// scheduleWorld is the schedules over real owners: a project created by its
// lifecycle, Work starting runs from what the project adopted (the real
// preparation), and the library a newer version is published to.
type scheduleWorld struct {
	schedules changeactions.Schedules
	work      *workflow.Service
	ways      *wow.Service
	adoption  *adoption.Service
	project   project.Project
	engine    context.Context
}

func newScheduleWorld(t *testing.T) scheduleWorld {
	t.Helper()
	db := changeactions.DatabaseSuite.New(t)
	dataDir := t.TempDir()
	ids := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := ids.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(ids, "project"))
	ways := wow.New(db, wow.Dependencies{})
	adopt := adoption.New(db, adoption.Dependencies{Authority: access.Checked{Authority: identity.Scoped(ids, "adoption"), Projects: projects}, Ways: ways})
	locks := new(scm.RepositoryLocks)
	authority := access.Checked{Authority: identity.Scoped(ids, "work"), Projects: projects}
	lifecycle := projectlifecycle.New(db, projectlifecycle.Dependencies{Adoption: adopt, Projects: projects, Ways: ways, Repositories: locks, Changes: workflow.NewChanges(db, authority, nil)}, projectlifecycle.Config{DataDir: dataDir})
	p, err := lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "clock", Name: "Clock", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	preparation := workcommands.NewPreparation(db, adopt, scm.NewBranches(db, locks), dataDir, nil, nil, nil, nil)
	work := workflow.New(context.Background(), db, workflow.Dependencies{Authority: authority, Repositories: locks, Resolve: preparation.Resolve, LandingWork: preparation,
		Git: workflow.GitWorkspace{DataDir: dataDir}, DataDir: dataDir})
	t.Cleanup(work.Stop)
	engine := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "engine"}, Verified: true, Receipt: "system"})
	return scheduleWorld{schedules: changeactions.Schedules{Actions: changeactions.New(work, db), Adoption: adopt, Projects: projects, DB: db},
		work: work, ways: ways, adoption: adopt, project: *p, engine: engine}
}

// runs are the project's runs.
func (w scheduleWorld) runs(t *testing.T) []workflow.RunSummary {
	t.Helper()
	runs, err := w.work.Runs(w.engine, workflow.RunQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestScheduleUsesRecordedWorkflowAndExplicitIdentity(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: clock}
spec:
  kind: pipeline
  on:
    schedule: {cron: "* * * * *"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [bls], means: "the scheduled review is complete"}
`
	if name, version, err := w.ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil || name != "clock" || version != 1 {
		t.Fatalf("publish fixture workflow: %s@%d %v", name, version, err)
	}
	if err := w.adoption.SelectLane(t.Context(), w.project.ID, "clock", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.adoption.RecordCurrent(t.Context(), w.project.ID, adoption.AdoptionEdit, "fixture selects clock", "fixture"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	w.schedules.FireDueSchedules(ctx, base)
	if _, armed, err := workflow.ScheduleLastFiredIn(ctx, w.schedules.DB, w.project.ID, "clock"); err != nil || !armed {
		t.Fatalf("the adopted clock was not armed: %v", err)
	}
	if runs := w.runs(t); len(runs) != 0 {
		t.Fatalf("arming fired a run: %+v", runs)
	}
	// A newer library version changes the cron; the project's adopted clock stays.
	if name, version, err := w.ways.PublishWorkflow(ctx, strings.Replace(yaml, "* * * * *", "0 0 1 1 *", 1), "fixture"); err != nil || name != "clock" || version != 2 {
		t.Fatalf("new library version: %s@%d %v", name, version, err)
	}
	w.schedules.FireDueSchedules(ctx, base.Add(time.Minute))
	runs := w.runs(t)
	if len(runs) != 1 || runs[0].ProjectSlug != w.project.Slug || runs[0].WorkflowName != "clock" || runs[0].WorkflowVersion != 1 {
		t.Fatalf("the scheduled run: %+v", runs)
	}
	last, armed, err := workflow.ScheduleLastFiredIn(ctx, w.schedules.DB, w.project.ID, "clock")
	if err != nil || !armed {
		t.Fatalf("schedule: %v", err)
	}
	if !last.Time.Equal(base.Add(time.Minute)) {
		t.Fatalf("last firing: %v", last)
	}
	w.schedules.FireDueSchedules(ctx, base.Add(2*time.Minute))
	if runs := w.runs(t); len(runs) != 1 { // active run skips the next firing
		t.Fatalf("an active run did not skip the firing: %d runs", len(runs))
	}
}

// A firing whose run could not be started is still recorded: the schedule
// tries again at its next due time, not on every tick.
func TestAScheduledFiringIsRecordedWhenItsRunCannotStart(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: hourly}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 * * * *"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [bls], means: "the hourly review is complete"}
`
	if _, _, err := w.ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := w.adoption.SelectLane(t.Context(), w.project.ID, "hourly", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.adoption.RecordCurrent(t.Context(), w.project.ID, adoption.AdoptionEdit, "fixture selects hourly", "fixture"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	w.schedules.FireDueSchedules(ctx, base) // armed
	due := base.Add(time.Hour)
	clear := w.schedules.DB.FailNextStatement("INSERT INTO steps")
	w.schedules.FireDueSchedules(ctx, due)
	clear()
	if runs := w.runs(t); len(runs) != 0 {
		t.Fatalf("a run started though its admission failed: %+v", runs)
	}
	last, _, err := workflow.ScheduleLastFiredIn(ctx, w.schedules.DB, w.project.ID, "hourly")
	if err != nil || !last.Time.Equal(due) {
		t.Fatalf("the failed firing was not recorded: %v %v", last, err)
	}
	w.schedules.FireDueSchedules(ctx, due.Add(time.Minute))
	if runs := w.runs(t); len(runs) != 0 {
		t.Fatalf("the schedule fired again before its next due time: %+v", runs)
	}
}

// An eval's throwaway copy runs only what its eval starts in it: a
// schedule it carries from its source is neither armed nor fired.
func TestAnEvalCopyFiresNoSchedule(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	// The copy is made as CreateCopy makes one: its purpose with the row,
	// and an adoption it can select the source's lane in.
	copyID, err := database.Transact(t.Context(), w.schedules.DB, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "clock-copy", Name: "Clock copy", Purpose: project.EvalCopy})
		if err != nil {
			return ids.Nil, err
		}
		if err := adoption.InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return ids.Nil, err
		}
		return id, adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "eval copy created", adoption.EmptyReceipt(), "eval/1", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: clock}
spec:
  kind: pipeline
  on:
    schedule: {cron: "* * * * *"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [bls], means: "the scheduled review is complete"}
`
	if _, _, err := w.ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []ids.ID{w.project.ID, copyID} {
		if err := w.adoption.SelectLane(t.Context(), id, "clock", true, "fixture"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.adoption.RecordCurrent(t.Context(), id, adoption.AdoptionEdit, "fixture selects clock", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	w.schedules.FireDueSchedules(ctx, base)
	if _, armed, err := workflow.ScheduleLastFiredIn(ctx, w.schedules.DB, w.project.ID, "clock"); err != nil || !armed {
		t.Fatalf("the source's schedule was not armed: %v", err)
	}
	w.schedules.FireDueSchedules(ctx, base.Add(time.Minute))
	if _, armed, err := workflow.ScheduleLastFiredIn(ctx, w.schedules.DB, copyID, "clock"); err != nil || armed {
		t.Fatalf("a copy's schedule was armed or fired: %v", err)
	}
}

// adoptScheduled publishes a workflow with an approval step that waits, so
// its run stays active, and selects it in the project.
func (w scheduleWorld) adoptScheduled(t *testing.T, name, cron string) {
	t.Helper()
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: ` + name + `}
spec:
  kind: pipeline
  on:
    schedule: {cron: "` + cron + `"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [bls], means: "the scheduled review is complete"}
`
	if _, _, err := w.ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := w.adoption.SelectLane(t.Context(), w.project.ID, name, true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.adoption.RecordCurrent(t.Context(), w.project.ID, adoption.AdoptionEdit, "fixture selects "+name, "fixture"); err != nil {
		t.Fatal(err)
	}
}

// schedule is the project's one schedule as its read shows it at now.
func (w scheduleWorld) schedule(t *testing.T, now time.Time) changeactions.ProjectSchedule {
	t.Helper()
	got, err := w.schedules.Of(t.Context(), w.project, now)
	if err != nil || got.EvalCopy || len(got.Schedules) != 1 {
		t.Fatalf("schedules: %+v %v", got, err)
	}
	return got.Schedules[0]
}

// A project's schedule read agrees with the scheduler: before arming, the
// next firing counts from now; once armed and never fired there is no last
// firing; a firing that opened a change names it, a skip says so, and the
// next firing counts from the last one, by the scheduler's own record.
func TestAScheduleShowsItsNextAndLastFiringAsTheSchedulerRecordedThem(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	w.adoptScheduled(t, "hourly", "0 * * * *")
	base := time.Date(2026, 9, 10, 0, 10, 0, 0, time.UTC)

	s := w.schedule(t, base)
	if s.Workflow != "hourly" || s.Version != 1 || s.Cron != "0 * * * *" || !s.SkipIfActive || s.Problem != "" {
		t.Fatalf("unarmed schedule: %+v", s)
	}
	if want := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC); !s.Next.Equal(want) || s.Due || s.Last != nil {
		t.Fatalf("before arming: next %v due %v last %+v, want next %v", s.Next, s.Due, s.Last, want)
	}

	w.schedules.FireDueSchedules(t.Context(), base) // armed, never fired
	if s := w.schedule(t, base.Add(time.Minute)); s.Last != nil || !s.Next.Equal(time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)) || s.Due {
		t.Fatalf("armed and never fired: %+v", s)
	}
	// Its time has passed and no tick has fired it yet: due, still unfired.
	if s := w.schedule(t, base.Add(time.Hour)); !s.Due || s.Last != nil {
		t.Fatalf("a passed firing time is not due: %+v", s)
	}

	opened := time.Date(2026, 9, 10, 1, 0, 30, 0, time.UTC)
	w.schedules.FireDueSchedules(t.Context(), opened)
	runs := w.runs(t)
	if len(runs) != 1 {
		t.Fatalf("the due firing opened %d runs", len(runs))
	}
	s = w.schedule(t, opened)
	if s.Last == nil || s.Last.Outcome != workflow.ScheduleOpened || s.Last.ChangeID != runs[0].ChangeID || s.Last.ChangeID.IsZero() || !s.Last.At.Equal(opened) {
		t.Fatalf("an opened firing: %+v (run %+v)", s.Last, runs[0])
	}
	// The firing names the change it opened by its number, as people read it.
	if s.Last.ChangeNumber <= 0 || s.Last.ChangeNumber != runs[0].ChangeNumber {
		t.Fatalf("an opened firing names change %d, want %d", s.Last.ChangeNumber, runs[0].ChangeNumber)
	}
	if want := time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC); !s.Next.Equal(want) || s.Due {
		t.Fatalf("next after firing: %v due %v, want %v", s.Next, s.Due, want)
	}

	// The opened run waits on its approval, so the next firing skips.
	skipped := time.Date(2026, 9, 10, 2, 0, 30, 0, time.UTC)
	w.schedules.FireDueSchedules(t.Context(), skipped)
	if s := w.schedule(t, skipped); s.Last == nil || s.Last.Outcome != workflow.ScheduleSkipped || !s.Last.ChangeID.IsZero() || s.Last.ChangeNumber != 0 || !s.Last.At.Equal(skipped) {
		t.Fatalf("a skipped firing: %+v", s.Last)
	}
}

// A firing whose run could not start is shown as that, not as opened or
// never fired.
func TestAScheduleShowsAFiringThatCouldNotStart(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	w.adoptScheduled(t, "hourly", "0 * * * *")
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	w.schedules.FireDueSchedules(t.Context(), base)
	due := base.Add(time.Hour)
	clear := w.schedules.DB.FailNextStatement("INSERT INTO steps")
	w.schedules.FireDueSchedules(t.Context(), due)
	clear()
	if s := w.schedule(t, due); s.Last == nil || s.Last.Outcome != workflow.ScheduleFailed || !s.Last.ChangeID.IsZero() {
		t.Fatalf("a firing that could not start: %+v", s.Last)
	}
}

// An eval copy says it runs no schedules rather than listing what it
// carries; a project whose workflows carry none lists none; and a failed
// read of the firing records fails the read rather than answering empty.
func TestAProjectsScheduleReadSaysCopyNoneOrFailed(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	if got, err := w.schedules.Of(t.Context(), w.project, time.Now()); err != nil || got.EvalCopy || len(got.Schedules) != 0 {
		t.Fatalf("no scheduled workflow: %+v %v", got, err)
	}
	w.adoptScheduled(t, "clock", "* * * * *")
	clear := w.schedules.DB.FailNextStatement("name: WorkflowSchedulesOfProject")
	_, err := w.schedules.Of(t.Context(), w.project, time.Now())
	clear()
	if err == nil {
		t.Fatal("a failed record read answered")
	}
	copied := w.project
	copied.Purpose = project.EvalCopy
	if got, err := w.schedules.Of(t.Context(), copied, time.Now()); err != nil || !got.EvalCopy || len(got.Schedules) != 0 {
		t.Fatalf("an eval copy: %+v %v", got, err)
	}
}

// The read counts a schedule's cron in UTC, as the scheduler does, whatever
// zone AppTown's clock reports its time in.
func TestAScheduleIsReadInUTCWhateverTheClocksZone(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	w.adoptScheduled(t, "monthly", "0 6 1 * *")
	melbourne := time.FixedZone("AEDT", 11*60*60)
	now := time.Date(2026, 10, 11, 14, 0, 0, 0, melbourne)
	want := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	if s := w.schedule(t, now); !s.Next.Equal(want) {
		t.Fatalf("before arming: next %v, want %v", s.Next, want)
	}
	w.schedules.FireDueSchedules(t.Context(), now)
	if s := w.schedule(t, now); !s.Next.Equal(want) {
		t.Fatalf("armed: next %v, want %v", s.Next, want)
	}
}

// A selected workflow whose stored definition no longer parses may carry a
// schedule: the read shows it as could-not-be-read, never as no schedule,
// and the scheduler still does not fire it.
func TestAnUnreadableWorkflowIsShownAsUnknownNotAsNoSchedule(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	w.adoptScheduled(t, "hourly", "0 * * * *")
	// Publishing validates a definition, so no operation stores one that
	// fails to parse; this stands for older stored content.
	// sql-guarantee: no owner operation can store an unparseable workflow.
	if _, err := w.schedules.DB.Exec(`UPDATE lib_workflow_versions SET yaml='spec: [' WHERE yaml LIKE '%name: hourly}%'`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s := w.schedule(t, base)
	if s.Workflow != "hourly" || s.Version != 1 || s.Problem == "" || !s.Next.IsZero() || s.Last != nil {
		t.Fatalf("an unreadable workflow: %+v", s)
	}
	w.schedules.FireDueSchedules(t.Context(), base)
	if _, armed, err := workflow.ScheduleLastFiredIn(t.Context(), w.schedules.DB, w.project.ID, "hourly"); err != nil || armed {
		t.Fatalf("an unreadable workflow was armed: %v", err)
	}
}
