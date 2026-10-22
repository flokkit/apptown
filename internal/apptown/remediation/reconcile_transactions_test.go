package remediation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// Reconciliation's persistence rules use its real attempt admission and Work
// claims. Later SQL mutations represent deliberately stale reconciliation facts.
func reconcileFixture(t *testing.T) (*database.DB, Reconciliation, Item) {
	t.Helper()
	f, store, item := reconcileScenario(t, ActionWait, workflow.StatusFail)
	return f.db, store, item
}
func reconcileScenario(t *testing.T, action Action, result workflow.StepState) (*attemptScenario, Reconciliation, Item) {
	t.Helper()
	f, starts, queued, def := attemptFixture(t, false)
	def.Run.Spec.Stages = append(def.Run.Spec.Stages, schema.Stage{Name: "next", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}})
	if action == ActionRecheckLanding {
		def.Run.Workflow.Name = "land"
	}
	run, err := starts.Start(context.Background(), queued, def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	item := f.item(t, queued.ID)
	if action != ActionPrepare {
		f.prepare(t, run.ID)
	}
	if action == ActionBackoff || action == ActionRecheckLanding {
		f.reason = "429"
	}
	f.execute(t, item, result)
	if action == ActionLanded {
		f.moveChange(t, item, workflow.ShipChange)
	}
	if action == ActionRecheckLanding {
		f.moveChange(t, item, workflow.LandChange)
	}
	return f, NewReconciliation(f.db, f.work, f.ctx, nil), f.item(t, queued.ID)
}

type reconcileActor struct{}

func (reconcileActor) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	c, _ := access.CallerFrom(ctx)
	return c, nil
}
func observeRemediation(t *testing.T, store Reconciliation, item Item) Observation {
	t.Helper()
	o, err := store.Observe(context.Background(), item)
	if err != nil || o == nil {
		t.Fatalf("observation: %+v %v", o, err)
	}
	return *o
}

func TestRemediationReadFailuresNeverBecomeDecisions(t *testing.T) {
	t.Parallel()
	// sql-guarantee: statement fragments for failNextStatement, not reads
	for _, fragment := range []string{"SET TRANSACTION", "FROM remediation_items WHERE id", "FROM changes WHERE id", "FROM runs WHERE id", "SELECT EXISTS(SELECT 1 FROM run_starts", "FROM steps WHERE run_id"} {
		t.Run(fragment, func(t *testing.T) {
			t.Parallel()
			db, store, item := reconcileFixture(t)
			before := outcomeSnapshot(t, db)
			r := NewReconciler(store,
				func(ids.ID) error { t.Error("prepared on failed read"); return nil },
				func(context.Context, Item) error { t.Error("reviewed on failed read"); return nil },
				func(ids.ID) { t.Error("dispatched on failed read") },
				func(string) { t.Error("cooled down on failed read") })
			dbtest.FailNextStatement(t, db, fragment)
			if err := r.Reconcile(context.Background(), item); err == nil {
				t.Fatal("read failure hidden")
			}
			if outcomeSnapshot(t, db) != before {
				t.Fatal("failed read changed state, history, accounting or lease")
			}
			if err := r.Reconcile(context.Background(), item); err != nil {
				t.Fatalf("explicit rerun: %v", err)
			}
		})
	}
}

func TestRemediationSnapshotIsConsistentAndUsesCurrentStage(t *testing.T) {
	t.Parallel()
	f, store, item := reconcileScenario(t, ActionWait, workflow.StatusFail)
	db := f.db
	tx, err := db.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Establish the snapshot before a concurrent workflow transition commits.
	if _, err := ReadItem(context.Background(), tx, item.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.work.Snapshot(f.ctx, item.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type == "script" {
			if _, err := f.work.RetryStep(f.ctx, workflow.RetryCommand{StepID: step.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.InTx(context.Background(), func(write *database.Tx) error {
		current, err := workflow.ReadRunIn(f.ctx, write, item.RunID)
		if err != nil {
			return err
		}
		if err := workflow.TransitionRunIn(f.ctx, write, current, workflow.RunRunning, "next", time.Now()); err != nil {
			return err
		}
		spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "next", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
		_, err = workflow.NewStages(nil, func(string) bool { return false }).EnterIn(f.ctx, write, item.RunID, &spec, "next", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	old, err := ReadObservation(context.Background(), tx, item)
	if err != nil {
		t.Fatal(err)
	}
	if old.Execution.Run.CurrentStage != "build" || old.Execution.Run.RetryRevision != 0 || Decide(*old).Action != ActionStuck {
		t.Fatalf("mixed snapshot: %+v", old)
	}
	current := observeRemediation(t, store, item)
	decision := Decide(current)
	if current.Execution.Run.CurrentStage != "next" || current.Execution.Run.RetryRevision != 1 || len(current.Execution.Steps) != 1 || decision.State != ItemAccepting {
		t.Fatalf("old-stage failure affected new stage: %+v %+v", current, decision)
	}
}

func TestRemediationMissingRecordsAreDistinctFromReadErrors(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"Change", "run"} {
		t.Run(missing, func(t *testing.T) {
			db, store, item := reconcileFixture(t)
			if missing == "Change" {
				// sql-guarantee: Remove only the admitted Change while retaining its run, item and history; owner deletion is project-wide and would erase the missing-Change observation.
				completionExec(t, db, `DELETE FROM changes`)
			} else {
				// sql-guarantee: Remove only the tracked run while retaining its Change, item and history; no Workflow operation deletes that run alone.
				completionExec(t, db, `DELETE FROM runs`)
			}
			o := observeRemediation(t, store, item)
			decision := Decide(o)
			if decision.Action != ActionStuck || decision.Reason != "its "+missing+" disappeared" {
				t.Fatalf("missing %s: %+v", missing, decision)
			}
			if result, err := store.Apply(context.Background(), o, decision); err != nil || result == nil {
				t.Fatalf("record absence: %+v %v", result, err)
			}
			oneItem(t, db, item.ID, func(i Item) bool { return i.State == ItemStuck })
		})
	}
}

func TestRemediationDecisionsRejectChangedWorkflowFacts(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		// sql-guarantee: Isolate retry revision in a captured full Run comparison; RetryStep also changes step facts and run updated_at, masking a missing revision fence.
		`UPDATE runs SET retry_revision=retry_revision+1`,
		// sql-guarantee: Isolate current stage in ExecutionObservation.Same; TransitionRunIn also changes compared updated_at and would mask a missing stage fence.
		`UPDATE runs SET current_stage='next'`,
		// sql-guarantee: Rewrite the same admitted run declaration after observation; no Workflow operation mutates a pinned definition.
		`UPDATE runs SET definition_json='{"changed":true}'`,
		// sql-guarantee: Rewrite the same admitted run inputs after observation; no Workflow operation mutates pinned inputs.
		`UPDATE runs SET inputs_json='{"changed":true}'`,
		// sql-guarantee: Isolate lifecycle in the full Run snapshot; TransitionRunIn also changes compared updated_at and would mask a missing status fence.
		`UPDATE runs SET status='done'`,
		// sql-guarantee: Invalidate a completed preparation receipt after observation; no Workflow operation reverses ready while retaining that same prepared execution.
		`UPDATE run_starts SET ready=false`,
		// sql-guarantee: Isolate the completed script verdict in the full step snapshot; another completion or override also changes compared timestamps or override fields.
		`UPDATE steps SET status='pass' WHERE type='script'`,
		// sql-guarantee: Isolate the completed script attempt in the full step snapshot; an actual retry/claim also changes status, evidence and timestamps.
		`UPDATE steps SET attempt=attempt+1 WHERE type='script'`,
		// sql-guarantee: Add override attribution to the same failed step without passing it; Override changes the compared verdict and would mask an override-field fence.
		`UPDATE steps SET override_by='alice' WHERE type='script'`,
		// sql-guarantee: Replace evidence on an already completed attempt without changing its compared finish stamp; supported completion requires a newly claimed attempt.
		`UPDATE steps SET evidence_json='{"reason":"429"}' WHERE type='script'`,
		// sql-guarantee: Replace configuration on the same completed attempt; RetryStep also clears its compared status, evidence and timestamps, masking the configuration fence.
		`UPDATE steps SET config_json='{"command":"new"}' WHERE type='script'`,
		`ship Change`,
		`invalidate item outcome`,
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			f, store, item := reconcileScenario(t, ActionWait, workflow.StatusFail)
			db := f.db
			o := observeRemediation(t, store, item)
			decision := Decide(o)
			switch mutation {
			case "ship Change":
				f.moveChange(t, item, workflow.ShipChange)
			case "invalidate item outcome":
				requireStoredWrite(t)(queries(db).SetItemState(f.ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason}))
			default:
				completionExec(t, db, mutation)
			}
			before := outcomeSnapshot(t, db)
			if result, err := store.Apply(context.Background(), o, decision); err != nil || result != nil {
				t.Fatalf("stale decision: %+v %v", result, err)
			}
			if outcomeSnapshot(t, db) != before {
				t.Fatal("stale decision wrote state or renewed a lease")
			}
		})
	}
}

func TestRemediationApplyReadFailureAndPhaseReceiptRollback(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"FROM changes WHERE id", "FROM steps WHERE run_id", "INSERT INTO audit_log"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f, store, item := reconcileScenario(t, ActionWait, workflow.StatusPass)
			db := f.db
			o := observeRemediation(t, store, item)
			before := outcomeSnapshot(t, db)
			dbtest.FailNextStatement(t, db, failure)
			if result, err := store.Apply(context.Background(), o, Decide(o)); err == nil || result != nil {
				t.Fatalf("failure hidden: %+v %v", result, err)
			}
			if outcomeSnapshot(t, db) != before {
				t.Fatal("failed apply left partial state or lease")
			}
			if result, err := store.Apply(context.Background(), o, Decide(o)); err != nil || result == nil {
				t.Fatalf("explicit rerun: %+v %v", result, err)
			}
		})
	}
}

func TestRemediationStaleActionsNeverStartExternalWork(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{ActionPrepare, ActionVerify, ActionBackoff, ActionLanded, ActionRecheckLanding} {
		t.Run(map[Action]string{ActionPrepare: "prepare", ActionVerify: "verify", ActionBackoff: "backoff", ActionLanded: "landed", ActionRecheckLanding: "recheck"}[action], func(t *testing.T) {
			f, store, item := reconcileScenario(t, action, workflow.StatusFail)
			db := f.db
			if action == ActionVerify || action == ActionRecheckLanding {
				state := ItemVerifying
				if action == ActionRecheckLanding {
					state = ItemLanding
				}
				requireStoredWrite(t)(queries(db).SetItemState(f.ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: state, Reason: item.Reason}))
				item = f.item(t, item.ID)
			}
			observed := observeRemediation(t, store, item)
			if got := Decide(observed); got.Action != action {
				t.Fatalf("decision: %+v", got)
			}
			var before string
			r := NewReconciler(interceptedReconciliation{store, func() {
				requireStoredWrite(t)(queries(db).SetItemState(f.ctx, remediationsql.SetItemStateParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason}))
				before = outcomeSnapshot(t, db)
			}},
				func(ids.ID) error { t.Error("stale preparation"); return nil },
				func(context.Context, Item) error { t.Error("stale verification"); return nil },
				func(ids.ID) { t.Error("stale dispatch") },
				func(string) { t.Error("stale cooldown") })
			if err := r.Reconcile(context.Background(), item); err != nil {
				t.Fatal(err)
			}
			if outcomeSnapshot(t, db) != before {
				t.Fatal("stale action changed persisted state")
			}
		})
	}
}

type interceptedReconciliation struct {
	Reconciliation
	beforeApply func()
}

func (s interceptedReconciliation) Apply(ctx context.Context, o Observation, d Decision) (*Applied, error) {
	s.beforeApply()
	return s.Reconciliation.Apply(ctx, o, d)
}

func TestRemediationPhaseChangedOnce(t *testing.T) {
	t.Parallel()
	f, _, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.LandChange)
	store := NewReconciliation(f.db, f.work, f.ctx, nil)
	observed := observeRemediation(t, store, item)
	decision := Decide(observed)
	results := race(5, func(int) int {
		result, err := store.Apply(context.Background(), observed, decision)
		if err != nil {
			t.Error(err)
		}
		if result != nil {
			return 1
		}
		return 0
	})
	accepted := 0
	for _, result := range results {
		if result == 1 {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("exactly one phase change is accepted: %v", results)
	}
	receiptCount(t, f.db, 1, "remediation.item.phase")
}

func TestRemediationBlockedBuildGatePreservesFailureReason(t *testing.T) {
	t.Parallel()
	f, starts, item, def := attemptFixture(t, false)
	def.Run.Spec.Stages[0].OnEnter[0].Name = "preview"
	run, err := starts.Start(context.Background(), item, def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	f.prepare(t, run.ID)
	item = f.item(t, item.ID)
	f.reason = "exec format error: the pinned image is linux/arm64"
	f.execute(t, item, workflow.StatusFail)
	store := NewReconciliation(f.db, f.work, f.ctx, nil)
	o := observeRemediation(t, store, f.item(t, item.ID))
	d := Decide(o)
	if d.Action != ActionStuck || !strings.Contains(d.Reason, "cannot rework") || !strings.Contains(d.Reason, "arm64") {
		t.Fatalf("decision: %+v", d)
	}
	if result, err := store.Apply(context.Background(), o, d); err != nil || result == nil {
		t.Fatalf("apply: %+v %v", result, err)
	}
	current := f.item(t, item.ID)
	if current.State != ItemStuck || !strings.Contains(current.Reason, "arm64") {
		t.Fatalf("stuck: %+v", current)
	}
}

func TestRemediationDecisionCannotUndoManualStepRetry(t *testing.T) {
	t.Parallel()
	f, _, item, _ := outcomeFixture(t)
	finished := make(chan struct{})
	dispatchErrors := make(chan error, 1)
	prepared := workflow.New(context.Background(), f.db, workflow.Dependencies{Authority: reconcileActor{}, Abandonment: attemptEffects{}, Automatic: []string{"script"}, Git: remediationPreparationGit{}, Repositories: &scm.RepositoryLocks{}, Report: func(err error) { dispatchErrors <- err }, Runners: map[string]workflow.Runner{"script": func(e *workflow.Execution) {
		if _, err := e.Claim(); err != nil {
			t.Fatal(err)
		}
		if err := e.Finish(workflow.StepOutcome{Status: workflow.StatusFail, Evidence: []byte(`{"reason":"tests failed"}`)}, nil); err != nil {
			t.Fatal(err)
		}
		close(finished)
	}}})
	t.Cleanup(func() {
		done := prepared.BeginStop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("prepared Work did not drain")
		}
	})
	if err := prepared.DispatchStarted(item.RunID); err != nil {
		t.Fatal(err)
	}
	awaitRemediationSignal(t, finished)
	prepared.Wait()
	select {
	case err := <-dispatchErrors:
		if !strings.Contains(err.Error(), "current advancement checks are unavailable") {
			t.Fatalf("unexpected dispatch error: %v", err)
		}
	default:
		t.Fatal("failed step did not report its expected unavailable advancement")
	}
	prepared.Stop()
	f.work = prepared
	store := NewReconciliation(f.db, prepared, f.ctx, nil)
	observed := observeRemediation(t, store, item)
	decision := Decide(observed)
	if decision.Action != ActionStuck {
		t.Fatalf("decision: %+v", decision)
	}
	snapshot, err := f.work.Snapshot(f.ctx, item.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var stepID ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "script" {
			stepID = step.ID
		}
	}
	if stepID.IsZero() {
		t.Fatal("missing script step")
	}
	if _, err := f.work.RetryStep(f.ctx, workflow.RetryCommand{StepID: stepID}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	if result, err := store.Apply(context.Background(), observed, decision); err != nil || result != nil {
		t.Fatalf("pre-retry decision: %+v %v", result, err)
	}
	if f.snapshot(t) != before {
		t.Fatal("old failure overwrote retried work")
	}
	fresh := observeRemediation(t, store, item)
	if fresh.Execution.Run.RetryRevision <= observed.Execution.Run.RetryRevision || Decide(fresh).Action != ActionWait {
		t.Fatalf("fresh observation did not see retry: %+v", fresh)
	}
}

type remediationPreparationGit struct{}

func (remediationPreparationGit) EnsureClone(context.Context, workflow.CloneRequest) (workflow.WorkspaceResult, error) {
	return workflow.WorkspaceResult{Tip: "original-sha"}, nil
}
func (remediationPreparationGit) SyncAttachments(context.Context, ids.ID, string) error { return nil }
func (remediationPreparationGit) BringToBranch(context.Context, string, string, string) error {
	return nil
}
func (remediationPreparationGit) BranchTip(context.Context, string, string) (string, error) {
	return "original-sha", nil
}
func (remediationPreparationGit) MergeBase(context.Context, string, string, string) (string, error) {
	return "original-sha", nil
}
func (remediationPreparationGit) Paths() workflow.Layout { return workflow.Layout{} }

// This command-only double deliberately cannot authorize aggregate reads.
func (reconcileActor) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}
