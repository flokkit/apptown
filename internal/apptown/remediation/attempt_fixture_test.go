package remediation

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// This fixture assembles only the participating owners. Saved findings and
// retries use their actual admission paths; no App or external work is started.
type attemptScenario struct {
	db        *database.DB
	work      *workflow.Service
	ctx       context.Context
	result    workflow.StepState
	reason    string
	execution *workflow.Execution
	projects  []ids.ID         // every project the scenario created, so lists across them are totals
	now       func() time.Time // the clock its jobs and outcomes are written by; nil the real one
}

func attemptFixture(t *testing.T, previous bool) (*attemptScenario, Attempts, Item, StartDefinition) {
	t.Helper()
	f := &attemptScenario{db: testDB(t), ctx: access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}, Verified: true, Receipt: "system"})}
	// Work decides only for a person with an enabled account.
	if _, err := identity.New(f.db, identity.Config{}).EnsureUser(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	f.work = workflow.New(context.Background(), f.db, workflow.Dependencies{Repositories: &scm.RepositoryLocks{}, Authority: reconcileActor{}, Abandonment: attemptEffects{}, Automatic: []string{"script"}, Runners: map[string]workflow.Runner{"script": func(e *workflow.Execution) {
		if _, err := e.Claim(); err != nil {
			t.Fatal(err)
		}
		f.execution = e
		if f.result != workflow.StatusRunning {
			reason := f.reason
			if reason == "" {
				reason = "tests failed"
			}
			if err := e.Finish(workflow.StepOutcome{Status: f.result, Evidence: []byte(`{"reason":` + strconv.Quote(reason) + `}`)}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}}})
	f.work.Stop() // explicit ExecuteStep drives each scenario, never background dispatch
	p := f.project(t, "attempt")
	items := f.job(t, 0, p)
	def := StartDefinition{Title: "Fix the check", Body: "Full feedback", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{{Block: "hygiene", Version: 1, Check: "clean"}}}, Base: "original-sha", Run: workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", OnEnter: []schema.Step{{Name: "work", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}}
	starts := NewAttempts(f.db, f.work, f.ctx, nil)
	item := items[0]
	if previous {
		if run, err := starts.Start(context.Background(), item, def, "previous-worker"); err != nil || run == nil {
			t.Fatalf("previous start: %+v %v", run, err)
		}
		item = f.item(t, item.ID)
		f.execute(t, item, workflow.StatusFail)
		if current, err := NewOutcomes(f.db, nil).Record(context.Background(), item, Outcome{Kind: "stuck", Reason: "failed"}); err != nil || current == nil {
			t.Fatalf("previous outcome: %+v %v", current, err)
		}
		if err := starts.Retry(context.Background(), item.RemediationID, item.ID, "alice", "retry feedback"); err != nil {
			t.Fatal(err)
		}
		item = f.item(t, item.ID)
	}
	return f, starts, item, def
}

func (f *attemptScenario) project(t *testing.T, slug string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(f.db, nil).SetDefaultBranch(f.ctx, id, "main"); err != nil {
		t.Fatal(err)
	}
	f.projects = append(f.projects, id)
	return id
}

// changes is every Change in the scenario: Work's uncapped list for each
// project the scenario created (a Change cannot exist outside a project).
func (f *attemptScenario) changes(t *testing.T) []workflow.ChangeSummary {
	t.Helper()
	var out []workflow.ChangeSummary
	for _, p := range f.projects {
		page, err := f.work.Changes(f.ctx, p, workflow.ChangeList{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page...)
	}
	return out
}

func (f *attemptScenario) changeState(t *testing.T, id ids.ID) workflow.ChangeState {
	t.Helper()
	c, err := f.work.Change(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c.State
}

func (f *attemptScenario) runStatus(t *testing.T, id ids.ID) workflow.RunState {
	t.Helper()
	r, err := f.work.Run(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return r.Status
}

// trace is the run's full record: every step and every event, unbounded.
func (f *attemptScenario) trace(t *testing.T, runID ids.ID) *workflow.RunSnapshot {
	t.Helper()
	snap, err := f.work.Snapshot(f.ctx, runID, workflow.TraceView)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func (f *attemptScenario) step(t *testing.T, id ids.ID) *workflow.Step {
	t.Helper()
	s, err := workflow.ReadStepIn(f.ctx, f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// approvals is the run's approval steps.
func (f *attemptScenario) approvals(t *testing.T, runID ids.ID) []*workflow.Step {
	t.Helper()
	var out []*workflow.Step
	for _, s := range f.trace(t, runID).Steps {
		if s.Type == "approval" {
			out = append(out, s)
		}
	}
	return out
}

func (f *attemptScenario) eventCount(t *testing.T, runID ids.ID, kind string) int {
	t.Helper()
	n := 0
	for _, e := range f.trace(t, runID).Events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (f *attemptScenario) job(t *testing.T, concurrency int, projects ...ids.ID) []Item {
	t.Helper()
	checks := conformance.New(f.db, reconcileActor{}, attemptChecks{}, nil)
	e, err := checks.BeginReview(f.ctx, conformance.ReviewRequest{Trigger: "manual", Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	var findings []conformance.Observation
	for _, p := range projects {
		pending, err := e.PrepareVerdict(f.ctx, conformance.Assessment{ProjectID: p, Check: conformance.CheckIdentity{Block: "hygiene", Version: 1, Check: "clean"}, State: conformance.DoesNotConform, Detail: "tests failed"})
		if err != nil {
			t.Fatal(err)
		}
		verdict, err := pending.Commit(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		findings = append(findings, conformance.Recorded(verdict))
	}
	if _, err := e.Finish(f.ctx, conformance.Completion{Projects: len(projects), ProjectsDone: len(projects)}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	_, items, err := NewCreations(f.db, f.now).Create(f.ctx, CreationRequest{Scope: "fleet", Actor: "alice", Concurrency: concurrency, Findings: findings})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (f *attemptScenario) item(t *testing.T, id ids.ID) Item {
	t.Helper()
	item, err := ReadItem(context.Background(), f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *attemptScenario) execute(t *testing.T, item Item, status workflow.StepState) {
	t.Helper()
	// Execution follows actual preparation, even when dispatch is stopped.
	f.prepare(t, item.RunID)
	f.result = status
	snap, err := f.work.Snapshot(f.ctx, item.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snap.Steps {
		if step.Type == "script" {
			if err := f.work.ExecuteStep(f.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing script step")
}

func (f *attemptScenario) prepare(t *testing.T, runID ids.ID) {
	t.Helper()
	prepareRemediationRun(t, f.db, runID)
}

func prepareRemediationRun(t *testing.T, db *database.DB, runID ids.ID) {
	t.Helper()
	var reported error
	locks := &scm.RepositoryLocks{}
	work := workflow.New(context.Background(), db, workflow.Dependencies{Git: attemptGit{t.TempDir()}, Repositories: locks, Report: func(err error) { reported = err }})
	if err := work.DispatchStarted(runID); err != nil {
		t.Fatal(err)
	}
	work.Wait()
	work.Stop()
	if reported != nil {
		t.Fatal(reported)
	}
}

type attemptGit struct{ dir string }

func (g attemptGit) EnsureClone(context.Context, workflow.CloneRequest) (workflow.WorkspaceResult, error) {
	return workflow.WorkspaceResult{Path: g.dir, Tip: "original-sha"}, nil
}
func (attemptGit) SyncAttachments(context.Context, ids.ID, string) error       { return nil }
func (attemptGit) BringToBranch(context.Context, string, string, string) error { return nil }
func (attemptGit) BranchTip(context.Context, string, string) (string, error) {
	return "original-sha", nil
}
func (attemptGit) MergeBase(context.Context, string, string, string) (string, error) {
	return "original-sha", nil
}
func (g attemptGit) Paths() workflow.Layout { return workflow.Layout{DataDir: g.dir} }

type attemptChecks struct{}

func (attemptChecks) Head(context.Context, conformance.ProjectSource) (conformance.SourceHead, error) {
	return conformance.SourceHead{SHA: "original-sha"}, nil
}
func (attemptChecks) HistoricalChecksIn(context.Context, database.Queryer, []conformance.CheckVersion) ([]conformance.DeclaredCheck, error) {
	return []conformance.DeclaredCheck{{Block: "hygiene", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "clean", Sh: "false"}}}, nil
}
func (s attemptChecks) ChecksIn(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID][]conformance.DeclaredCheck, error) {
	checks, err := s.HistoricalChecksIn(ctx, q, nil)
	out := make(map[ids.ID][]conformance.DeclaredCheck, len(projects))
	for _, id := range projects {
		out[id] = checks
	}
	return out, err
}

// Cleanup scheduling is tested with the real AppTown participant in the retained
// atomic supersession tests. These owner tests have no external cleanup work.
type attemptEffects struct{}

func (attemptEffects) RecordAbandonIn(context.Context, *database.Tx, workflow.AbandonEffects) error {
	return nil
}

func (f *attemptScenario) retryExecution(t *testing.T, item Item, status workflow.StepState) {
	t.Helper()
	snap, err := f.work.Snapshot(f.ctx, item.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snap.Steps {
		if step.Type == "script" {
			if _, err := f.work.RetryStep(f.ctx, workflow.RetryCommand{StepID: step.ID}); err != nil {
				t.Fatal(err)
			}
			f.execute(t, item, status)
			return
		}
	}
	t.Fatal("missing script step")
}

func (f *attemptScenario) outcome(t *testing.T, item Item, result Outcome) Item {
	t.Helper()
	current, err := NewOutcomes(f.db, f.now).Record(context.Background(), item, result)
	if err != nil || current == nil {
		t.Fatalf("outcome: %+v %v", current, err)
	}
	return *current
}

func (f *attemptScenario) moveChange(t *testing.T, item Item, action workflow.ChangeAction) {
	t.Helper()
	if err := f.db.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := queries(tx).LockProjectForRemediation(f.ctx, item.ProjectID); err != nil {
			return err
		}
		move := workflow.ChangeMove{Action: action}
		if action == workflow.LandChange {
			move.RunID = item.RunID
		}
		_, err := workflow.TransitionChangeIn(f.ctx, tx, item.ProjectID, item.ChangeID, move, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Include Work's rows independently: a refused admission/control must not leave
// orphaned work or withdrawal receipts even if the item itself stayed unchanged.
func (f *attemptScenario) snapshot(t *testing.T) string {
	t.Helper()
	var work string
	// sql-guarantee: rollback proof — Work's raw rows (including run_starts, which no read exposes) must be byte-identical after a refused command.
	if err := f.db.SQL().QueryRowContext(context.Background(), `SELECT jsonb_build_object(
  'changes',(SELECT jsonb_agg(c ORDER BY id) FROM changes c),
  'runs',(SELECT jsonb_agg(r ORDER BY id) FROM runs r),
  'steps',(SELECT jsonb_agg(s ORDER BY id) FROM steps s),
  'events',(SELECT jsonb_agg(e ORDER BY id) FROM run_events e),
  'starts',(SELECT jsonb_agg(s ORDER BY run_id) FROM run_starts s))::text`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	return outcomeSnapshot(t, f.db) + work
}
