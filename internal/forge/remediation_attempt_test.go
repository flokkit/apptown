package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/schema"
)

func remediationAttemptFixture(t *testing.T, previous bool) (*App, remediation.Attempts, remediation.Item, remediation.StartDefinition) {
	t.Helper()
	a := testApp(t)
	projectID, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "attempt", Name: "Attempt"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(a.DB, nil).SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	testAccounts(t, a.DB, "alice")
	ctx := asPerson("alice")
	checks := conformance.New(a.DB, testActor{}, remediationFixtureChecks{}, nil)
	review, err := checks.BeginReview(ctx, conformance.ReviewRequest{Trigger: "manual", Projects: []ids.ID{projectID}})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := review.PrepareVerdict(ctx, conformance.Assessment{ProjectID: projectID, Check: conformance.CheckIdentity{Block: "hygiene", Version: 1, Check: "clean"}, State: conformance.DoesNotConform, Detail: "retry feedback"})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := pending.Commit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := review.Finish(ctx, conformance.Completion{Projects: 1, ProjectsDone: 1}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	_, items, err := remediation.NewCreations(a.DB, nil).Create(ctx, remediation.CreationRequest{Scope: "project", Actor: "alice", Findings: []conformance.Observation{conformance.Recorded(verdict)}})
	if err != nil || len(items) != 1 {
		t.Fatalf("remediation creation: items=%+v err=%v", items, err)
	}
	def := remediation.StartDefinition{Title: "Fix the check", Body: "Full feedback", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{{Block: "hygiene", Version: 1, Check: "clean"}}}, Base: "original-sha",
		Run: workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", OnEnter: []schema.Step{{Name: "work", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}}
	store := remediation.NewAttempts(a.DB, a.Work, asPerson("alice"), nil)
	item := items[0]
	if previous {
		installFakeGit(t, a, nil)
		d := installDispatchFixture(t, a)
		d.runners["script"] = func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"failed"}`)}, nil); err != nil {
				t.Fatal(err)
			}
		}
		store = remediation.NewAttempts(a.DB, a.Work, asPerson("alice"), nil)
		run, err := store.Start(ctx, item, def, "previous-worker")
		if err != nil || run == nil {
			t.Fatalf("previous admission: run=%+v err=%v", run, err)
		}
		if err := a.Work.DispatchStarted(run.ID); err != nil {
			t.Fatal(err)
		}
		d.wait()
		snap, err := a.Work.Snapshot(a.asEngine(context.Background()), run.ID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		var scriptStep *workflow.Step
		for _, step := range snap.Steps {
			if step.Type == "script" {
				scriptStep = step
			}
		}
		if scriptStep == nil || scriptStep.Status != workflow.StatusFail {
			t.Fatalf("failed script prerequisite: %+v", scriptStep)
		}
		err = a.DB.InTx(ctx, func(tx *database.Tx) error {
			_, err := a.Work.AdmitRunIn(a.asEngine(context.Background()), tx, workflow.StartRequest{ProjectID: projectID, ChangeID: run.ChangeID, WorkflowName: "child", InputsJSON: `{}`, RequestedBy: "alice", ParentRunID: run.ID}, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "build"}}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		item, err = remediation.ReadItem(ctx, a.DB, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := remediation.NewOutcomes(a.DB, nil).Record(ctx, item, remediation.Outcome{Kind: "stuck", Reason: "failed"})
		if err != nil || current == nil {
			t.Fatalf("previous outcome: item=%+v err=%v", current, err)
		}
		if err := store.Retry(context.Background(), item.RemediationID, item.ID, "alice", "retry feedback"); err != nil {
			t.Fatal(err)
		}
		oldChange, oldRun := current.ChangeID, current.RunID
		item, err = remediation.ReadItem(ctx, a.DB, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if item.ChangeID != oldChange || item.RunID != oldRun {
			t.Fatalf("retry lost prior attempt associations: item=%+v change=%d run=%d", item, oldChange, oldRun)
		}
	}
	return a, store, item, def
}

type remediationFixtureChecks struct{}

func (remediationFixtureChecks) Head(context.Context, conformance.ProjectSource) (conformance.SourceHead, error) {
	return conformance.SourceHead{SHA: "original-sha"}, nil
}
func (remediationFixtureChecks) HistoricalChecksIn(context.Context, database.Queryer, []conformance.CheckVersion) ([]conformance.DeclaredCheck, error) {
	return []conformance.DeclaredCheck{{Block: "hygiene", Version: 1, Check: conformance.CheckDeclaration{Name: "clean", Sh: "false"}}}, nil
}
func (s remediationFixtureChecks) ChecksIn(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID][]conformance.DeclaredCheck, error) {
	checks, err := s.HistoricalChecksIn(ctx, q, nil)
	out := make(map[ids.ID][]conformance.DeclaredCheck, len(projects))
	for _, id := range projects {
		out[id] = checks
	}
	return out, err
}

// Superseding the previous Change schedules Forge's cleanup job inside
// remediation's start transaction. If that job cannot be inserted, the whole
// start rolls back: the previous Change, its runs, remediation's records and
// every receipt are as they were, nothing is dispatched, and the same start
// succeeds once the fault is gone. remediation's
// TestRemediationStartRollsBackSupersessionAndAllNewRecords covers the
// failures inside its own and Work's records.
func TestRemediationAttemptRollsBackWithForgesCleanupJob(t *testing.T) {
	t.Parallel()
	a, store, item, def := remediationAttemptFixture(t, true)
	beforeOutcome := outcomeSnapshot(t, a.DB)
	before := map[string]int{}
	for _, table := range []string{"river_job", "run_events", "audit_log", "run_starts"} {
		var n int
		// sql-guarantee: failed admission leaves no hidden row in these tables; only a whole-table count proves nothing was written.
		if err := a.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[table] = n
	}
	failNextJobInsert(t, a.DB)
	var dispatched bool
	starter := remediation.NewStarter(store, func(context.Context, remediation.Item) (remediation.StartDefinition, error) { return def, nil }, func(ids.ID) error { dispatched = true; return nil })
	if _, err := starter.Start(context.Background(), item, "worker"); err == nil {
		t.Fatal("required failure was hidden")
	}
	if dispatched {
		t.Fatal("uncommitted attempt dispatched")
	}
	requireChangeState(t, a, item.ChangeID, workflow.ChangeInProgress)
	if n := runsWithStatus(allRuns(t, a), workflow.RunRunning); n != 2 {
		t.Fatalf("%d running runs, want the previous attempt's two", n)
	}
	if got := outcomeSnapshot(t, a.DB); got != beforeOutcome {
		t.Fatal("failed admission changed remediation state, attempts, jobs, or audit")
	}
	if n := len(remediationAttempts(t, a, item.RemediationID)); n != 1 {
		t.Fatalf("%d attempts after a failed admission", n)
	}
	for _, table := range []string{"river_job", "run_events", "audit_log", "run_starts"} {
		// sql-guarantee: compared with the whole-table counts taken before.
		completionCount(t, a.DB, before[table], `SELECT COUNT(*) FROM `+table)
	}
	run, err := starter.Start(context.Background(), item, "worker")
	if err != nil || run == nil || !dispatched {
		t.Fatalf("explicit rerun: run=%+v dispatch=%v err=%v", run, dispatched, err)
	}
	if got, err := remediation.ReadItem(context.Background(), a.DB, item.ID); err != nil || got.State != "working" || got.Attempt != 2 || got.RunID != run.ID {
		t.Fatalf("rerun item: %+v %v", got, err)
	}
}

func TestRemediationAttemptConcurrentAdmissionAndSupersession(t *testing.T) {
	t.Parallel()
	a, store, item, def := remediationAttemptFixture(t, true)
	results := race(8, func(int) int {
		run, err := store.Start(context.Background(), item, def, "worker")
		if err != nil {
			t.Error(err)
		}
		if run != nil {
			return 1
		}
		return 0
	})
	if count(results, 1) != 1 {
		t.Fatalf("admissions: %v", results)
	}
	if old := requireChangeState(t, a, item.ChangeID, workflow.ChangeAbandoned); !strings.HasPrefix(old.Summary, "superseded") {
		t.Fatalf("superseded change summary: %q", old.Summary)
	}
	if runs := changeRuns(t, a, item.ChangeID); len(runs) != 2 || runs[0].Status != workflow.RunAbandoned || runs[1].Status != workflow.RunAbandoned {
		t.Fatalf("superseded change's runs: %+v", runs)
	}
	if n := len(operationJobs(t, a, workflowAbandonmentJob)); n != 1 {
		t.Fatalf("%d abandonment cleanup jobs", n)
	}
	current, err := remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil || current.State != "working" || current.Attempt != 2 || current.ChangeID == item.ChangeID || current.RunID == item.RunID {
		t.Fatalf("admitted item: %+v %v", current, err)
	}
	matching := 0
	for _, attempt := range remediationAttempts(t, a, item.RemediationID) {
		if attempt.N == current.Attempt && attempt.ChangeID == current.ChangeID && attempt.RunID == current.RunID && attempt.BaseSHA == "original-sha" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("%d attempts record the admitted change and run", matching)
	}
	// The project's only open change is the new attempt's, pinned to the check.
	for _, change := range projectChanges(t, a, item.ProjectID) {
		if change.State == workflow.ChangeInProgress && change.ID != current.ChangeID {
			t.Fatalf("another open change: %+v", change)
		}
	}
	admitted := requireChangeState(t, a, current.ChangeID, workflow.ChangeInProgress)
	// acceptance_json->'checks'->0->>'check', by exact keys.
	var checks []json.RawMessage
	rawChecks, _ := exactKey(json.RawMessage(admitted.AcceptanceJSON), "checks")
	if json.Unmarshal(rawChecks, &checks) != nil || len(checks) == 0 || exactText(checks[0], "check") != "clean" || !strings.HasPrefix(admitted.Branch, "apptown/change-") {
		t.Fatalf("admitted change: acceptance %s branch %q", admitted.AcceptanceJSON, admitted.Branch)
	}
	if n := len(ledgerEntries(t, a, ids.Nil, "remediation.item.started")); n != 2 {
		t.Fatalf("%d remediation.item.started receipts", n)
	}
	if !runUnready(t, a, current.ProjectID, current.ChangeID, current.RunID) {
		t.Fatal("admitted run is ready before its preparation")
	}
	// A stale resolver cannot fail the newly admitted work.
	if err := store.Failed(context.Background(), item, "late error"); err != nil {
		t.Fatal(err)
	}
	if got, err := remediation.ReadItem(context.Background(), a.DB, item.ID); err != nil || got.State != "working" || got.Reason != "" {
		t.Fatalf("stale failure reached the new attempt: %+v %v", got, err)
	}
}

func TestRemediationAttemptRecoveryUsesCommittedRun(t *testing.T) {
	t.Parallel()
	a, store, item, def := remediationAttemptFixture(t, false)
	starter := remediation.NewStarter(store, func(context.Context, remediation.Item) (remediation.StartDefinition, error) { return def, nil }, func(ids.ID) error { return errors.New("stopped before dispatch") })
	run, err := starter.Start(context.Background(), item, "worker")
	if err == nil || run == nil {
		t.Fatalf("committed start: %+v %v", run, err)
	}
	restarted := restartApp(t, a.DB)
	installFakeGit(t, restarted, nil)
	d := installDispatchFixture(t, restarted)
	var calls atomic.Int32
	d.runners["script"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(mustJSON(map[string]any{})), Details: json.RawMessage(mustJSON(nil))}, nil); err != nil {
			t.Error(err)
		}
	}
	d.recover()
	d.wait()
	if calls.Load() != 1 {
		t.Fatalf("recovered %d times", calls.Load())
	}
	if attempts := remediationAttempts(t, a, item.RemediationID); len(attempts) != 1 || attempts[0].RunID != run.ID {
		t.Fatalf("attempts after recovery: %+v", attempts)
	}
	requireSingleRunAndChange(t, a, item.ProjectID, run)
	// sql-guarantee: Work's readiness observation reports "not unready" alike for a prepared start and a missing one; only the stored start shows the recovered run was recorded ready.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM run_starts WHERE ready`)
	if duplicate, err := store.Start(context.Background(), item, def, "old-worker"); err != nil || duplicate != nil {
		t.Fatalf("old admission replay: %+v %v", duplicate, err)
	}
}

func TestRemediationAttemptPreparationRetriesInSameProcess(t *testing.T) {
	t.Parallel()
	a, _, item, def := remediationAttemptFixture(t, false)
	owner := remediation.NewStarter(remediationAttemptsFor(a), func(context.Context, remediation.Item) (remediation.StartDefinition, error) { return def, nil }, func(runID ids.ID) error { return a.Work.DispatchStarted(runID) })
	var preparations, executions atomic.Int32
	installFakeGit(t, a, func(in workflow.CloneRequest) (workflow.WorkspaceResult, error) {
		if preparations.Add(1) == 1 {
			return workflow.WorkspaceResult{}, errors.New("workspace temporarily unavailable")
		}
		return workflow.WorkspaceResult{Path: in.Workspace, Tip: "fake"}, nil
	})
	d := installDispatchFixture(t, a)
	d.runners["script"] = func(*workflow.Execution) { executions.Add(1) }
	run, err := owner.Start(context.Background(), item, "worker")
	if err != nil || run == nil {
		t.Fatalf("start: %+v %v", run, err)
	}
	d.wait()
	if executions.Load() != 0 {
		t.Fatal("ran before preparation succeeded")
	}
	if !runUnready(t, a, item.ProjectID, run.ChangeID, run.ID) {
		t.Fatal("failed preparation marked the run ready")
	}
	current, err := remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.ReconcileItem(context.Background(), current)
	d.wait()
	if preparations.Load() != 2 || executions.Load() != 1 {
		t.Fatalf("preparations=%d executions=%d", preparations.Load(), executions.Load())
	}
	// sql-guarantee: Work's readiness observation reports "not unready" alike for a prepared start and a missing one; only the stored start shows this run was recorded ready.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM run_starts WHERE ready AND run_id=?`, run.ID)
	if n := len(remediationAttempts(t, a, item.RemediationID)); n != 1 {
		t.Fatalf("%d attempts", n)
	}
	requireSingleRunAndChange(t, a, item.ProjectID, run)
}

func TestRemediationResolutionPinsAcceptanceAndFullFeedback(t *testing.T) {
	t.Parallel()
	a, store, item, _ := remediationAttemptFixture(t, false)
	dbtest.FailNextStatement(t, a.DB, "-- name: ProjectByID :one")
	if def, err := a.remediationDependencies().Resolve(context.Background(), item, a.Fixes.VerificationScope(item)); err == nil || def.Skip != "" {
		t.Fatalf("failed project lookup skipped remediation: %+v %v", def, err)
	}
	a.SyncStock(t.Context())
	adopt(t, a, item.ProjectID, "hygiene", 1, checksJSON(shCheck("clean", "true", "high")))
	if err := a.lifecycle.SelectLane(t.Context(), item.ProjectID, "quick-change", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	ad := currentAdoption(a.DB, item.ProjectID)
	if ad == nil {
		t.Fatal("missing adoption")
	}
	// Live imports drift after the receipt. Acceptance still follows the
	// same immutable version as workflow resolution.
	imports, err := a.Adoption.Imports(t.Context(), item.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	pins := make([]wow.ResolvedPin, 0, len(imports))
	for _, imported := range imports {
		pins = append(pins, wow.ResolvedPin{BlockID: imported.BlockID, Name: imported.Name, Version: imported.Version, Disabled: true})
	}
	if err := a.Adoption.PutImports(t.Context(), item.ProjectID, pins); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("Retain this feedback after a failed start. ", 60)
	installFakeGit(t, a, nil)
	initial, err := a.remediationDependencies().Resolve(context.Background(), item, a.Fixes.VerificationScope(item))
	if err != nil || initial.Skip != "" {
		t.Fatalf("initial resolution: %+v %v", initial, err)
	}
	if run, err := store.Start(context.Background(), item, initial, "first-worker"); err != nil || run == nil {
		t.Fatalf("initial attempt: %+v %v", run, err)
	}
	item, err = remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	stuck, err := remediation.NewOutcomes(a.DB, nil).Record(context.Background(), item, remediation.Outcome{Kind: "stuck", Reason: note})
	if err != nil || stuck == nil {
		t.Fatalf("recording failed feedback: %+v %v", stuck, err)
	}
	if err := store.Retry(context.Background(), item.RemediationID, item.ID, "alice", note); err != nil {
		t.Fatal(err)
	}
	item, err = remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	def, err := a.remediationDependencies().Resolve(context.Background(), item, a.Fixes.VerificationScope(item))
	if err != nil || def.Skip != "" {
		t.Fatalf("resolve: skip=%q err=%v", def.Skip, err)
	}
	if def.Run.AdoptionID != ad.ID {
		t.Fatalf("definition lost pinned adoption: got %d want %d", def.Run.AdoptionID, ad.ID)
	}
	if def.Acceptance == nil || len(def.Acceptance.Checks) != 1 || def.Acceptance.Checks[0].Check != "clean" {
		t.Fatalf("definition lost pinned check: %+v", def.Acceptance)
	}
	if !strings.Contains(def.Body, strings.TrimSpace(note)) {
		t.Fatalf("definition truncated full feedback: body bytes=%d note bytes=%d", len(def.Body), len(note))
	}
	// A new adoption after resolution rejects the whole transaction, including
	// the change, rather than mixing definitions from two revisions.
	a.lifecycle.NoteAdoption(t.Context(), item.ProjectID, "fixture changed after remediation resolution", "alice")
	if current := currentAdoption(a.DB, item.ProjectID); current == nil || current.Revision != ad.Revision+1 {
		t.Fatalf("fixture adoption did not advance: %+v", current)
	}
	beforeChange, beforeRun, beforeAttempt := item.ChangeID, item.RunID, item.Attempt
	if run, err := store.Start(context.Background(), item, def, "worker"); err == nil || run != nil {
		t.Fatalf("stale definition admitted: %+v %v", run, err)
	}
	def.Skip = "the project no longer adopts this check"
	if _, err := store.Start(context.Background(), item, def, "worker"); err == nil {
		t.Fatal("stale check removal skipped the item")
	}
	item, err = remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != remediation.ItemQueued || item.Attempt != beforeAttempt || item.ChangeID != beforeChange || item.RunID != beforeRun {
		t.Fatalf("stale resolution changed the queued attempt: %+v", item)
	}
	if n := len(projectChanges(t, a, item.ProjectID)); n != 1 {
		t.Fatalf("%d changes after a stale resolution", n)
	}
	if n := len(remediationAttempts(t, a, item.RemediationID)); n != 1 {
		t.Fatalf("%d attempts after a stale resolution", n)
	}
}

// requireSingleRunAndChange fails unless the run and its change are the only
// ones: a retry or recovery reused the committed admission.
func requireSingleRunAndChange(t *testing.T, a *App, projectID ids.ID, run *workflow.Run) {
	t.Helper()
	if runs := allRuns(t, a); len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("runs: %+v, want only %d", runs, run.ID)
	}
	if changes := projectChanges(t, a, projectID); len(changes) != 1 || changes[0].ID != run.ChangeID {
		t.Fatalf("changes: %+v, want only %d", changes, run.ChangeID)
	}
}

// A queued item whose check is judged only on the running deployment is
// skipped with what to do instead, never started as a change whose
// acceptance no landing can judge.
func TestARemediationItemOnAStandingOnlyCheckIsSkipped(t *testing.T) {
	t.Parallel()
	a, _, item, _ := remediationAttemptFixture(t, false)
	a.SyncStock(t.Context())
	adopt(t, a, item.ProjectID, "memory", 1, checksJSON(`{"name":"memory-within","evaluator":"runtime","probe":"memory-within","severity":"high","expects":"the deployment runs within its memory"}`))
	if err := a.lifecycle.SelectLane(t.Context(), item.ProjectID, "quick-change", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	installFakeGit(t, a, nil)
	item.Block, item.Check = "memory", "memory-within"
	def, err := a.remediationDependencies().Resolve(context.Background(), item, a.Fixes.VerificationScope(item))
	if err != nil || !strings.Contains(def.Skip, "judged only on the running deployment") || def.Acceptance != nil {
		t.Fatalf("an item on a standing-only check: %+v %v", def, err)
	}
}
