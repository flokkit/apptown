package forge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Composition fixtures admit their run and record real runner outcomes. Private
// selection edge states (overrides, old stages, optional skips) live in Work.
func landingRecheckFixture(t *testing.T) (*App, *workflow.Run, map[string]ids.ID) {
	t.Helper()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish",
		OnEnter: []schema.Step{{Name: "image", Uses: "scan"}, {Name: "deploy", Uses: "deploy"}},
		Steps:   []schema.Step{{Name: "tests", Uses: "tests"}, {Name: "passed", Uses: "scan"}, {Name: "accept", Uses: "approval"}},
	}}}
	a, ctx, ch := changeScenario(t, spec, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"scan", "deploy", "tests"}
		deps.Report = func(err error) {
			var verdict *workflow.StepError
			if !errors.As(err, &verdict) {
				t.Error(err)
			}
		}
		runner := func(task *workflow.Execution) {
			current := task.Step()
			if current.Name == "deploy" {
				t.Error("failed image admitted deployment")
				return
			}
			claim, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			state := workflow.StatusPass
			if claim.Name == "image" {
				state = workflow.StatusFail
			}
			if claim.Name == "tests" {
				state = workflow.StatusError
			}
			if err := task.Finish(workflow.StepOutcome{Status: state, Evidence: []byte(`{"reason":"provider overloaded"}`)}, nil); err != nil {
				t.Error(err)
			}
		}
		deps.Runners = map[string]workflow.Runner{"scan": runner, "deploy": runner, "tests": runner}
	})
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	a.Work.Stop()
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(ctx, tx, run.ProjectID, ch.ID, workflow.ChangeMove{Action: workflow.LandChange, RunID: run.ID}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	stepIDs := map[string]ids.ID{}
	for _, step := range snapshot.Steps {
		stepIDs[step.Name] = step.ID
	}
	return a, snapshot.Run, stepIDs
}

func landingRemediationFixture(t *testing.T, a *App, source *workflow.Run) (workflow.LandingRecheck, *workflow.Run) {
	t.Helper()
	ctx := asPerson("alice")
	if err := project.New(a.DB, nil).SetDefaultBranch(ctx, source.ProjectID, "main"); err != nil {
		t.Fatalf("set review source branch: %v", err)
	}
	checks := conformance.New(a.DB, testActor{}, remediationFixtureChecks{}, nil)
	review, err := checks.BeginReview(ctx, conformance.ReviewRequest{Trigger: "landing retry fixture", Projects: []ids.ID{source.ProjectID}})
	if err != nil {
		t.Fatalf("begin review: %v", err)
	}
	pending, err := review.PrepareVerdict(ctx, conformance.Assessment{ProjectID: source.ProjectID, Check: conformance.CheckIdentity{Block: "hygiene", Version: 1, Check: "clean"}, State: conformance.DoesNotConform, Detail: "provider overloaded"})
	if err != nil {
		t.Fatalf("prepare verdict: %v", err)
	}
	verdict, err := pending.Commit(ctx, nil)
	if err != nil {
		t.Fatalf("commit verdict: %v", err)
	}
	if _, err := review.Finish(ctx, conformance.Completion{Projects: 1, ProjectsDone: 1}, conformance.ReviewDone); err != nil {
		t.Fatalf("finish review: %v", err)
	}
	if _, err := a.Work.Abandon(ctx, workflow.StopWork{ChangeID: source.ChangeID, Reason: "replace the setup run with the remediation-owned landing"}); err != nil {
		t.Fatalf("retire setup run: %v", err)
	}
	_, items, err := remediation.NewCreations(a.DB, nil).Create(ctx, remediation.CreationRequest{Scope: "project", Actor: "alice", Findings: []conformance.Observation{conformance.Recorded(verdict)}})
	if err != nil || len(items) != 1 {
		t.Fatalf("remediation creation: items=%+v err=%v", items, err)
	}
	item := items[0]
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish",
		OnEnter: []schema.Step{{Name: "image", Uses: "scan"}, {Name: "deploy", Uses: "deploy"}},
		Steps:   []schema.Step{{Name: "tests", Uses: "tests"}, {Name: "passed", Uses: "scan"}, {Name: "accept", Uses: "approval"}},
	}}}
	prepared := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}
	started, err := remediation.NewAttempts(a.DB, a.Work, ctx, nil).Start(ctx, item, remediation.StartDefinition{Title: "Fix the landing", Base: "original-sha", Run: prepared}, "worker")
	if err != nil || started == nil {
		t.Fatalf("start remediation attempt: run=%+v err=%v", started, err)
	}
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	if err := a.Work.DispatchStarted(started.ID); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	a.Work.Stop()
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(ctx, tx, started.ProjectID, started.ChangeID, workflow.ChangeMove{Action: workflow.LandChange, RunID: started.ID}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	item, err = remediation.ReadItem(ctx, a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	reconciliation := remediation.NewReconciliation(a.DB, a.Work, a.asForge(context.Background(), started.ProjectID, "remediation"), nil)
	observed, err := reconciliation.Observe(ctx, item)
	if err != nil || observed == nil {
		t.Fatalf("observe landing attempt: %+v %v", observed, err)
	}
	applied, err := reconciliation.Apply(ctx, *observed, remediation.Decide(*observed))
	if err != nil || applied == nil {
		t.Fatalf("record landing phase: %+v %v", applied, err)
	}
	item, err = remediation.ReadItem(ctx, a.DB, item.ID)
	if err != nil || item.State != remediation.ItemLanding {
		t.Fatalf("read landing phase: %+v %v", item, err)
	}
	return workflow.LandingRecheck{Who: "alice", ChangeID: started.ChangeID, RunID: started.ID, Remediation: &workflow.LandingRetry{ItemID: item.ID, Attempt: item.Attempt, Reason: "provider overloaded"}}, started
}

func TestLandingRecheckRollbackIncludesRetryBudgetAndNeverDispatches(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"last receipt", "UPDATE runs SET retry_revision"} {
		t.Run(failure, func(t *testing.T) {
			a, run, _ := landingRecheckFixture(t)
			req, run := landingRemediationFixture(t, a, run)
			d := installDispatchFixture(t, a)
			var calls atomic.Int32
			for _, typ := range []string{"scan", "deploy", "tests"} {
				d.runners[typ] = func(*workflow.Execution) { calls.Add(1) }
			}
			if failure == "last receipt" {
				completionExec(t, a.DB, `ALTER TABLE run_events ADD CONSTRAINT reject_last_recheck CHECK (kind!='step.recheck' OR payload_json->>'step'!='tests')`)
			} else {
				dbtest.FailNextStatement(t, a.DB, failure)
			}
			if names, err := a.Work.RecheckLanding(a.asEngine(context.Background()), req); err == nil || len(names.Names) != 0 {
				t.Fatalf("failed batch: %v %v", names, err)
			}
			d.wait()
			if calls.Load() != 0 {
				t.Fatal("uncommitted work was dispatched")
			}
			requireSteps(t, a, 0, func(s *workflow.Step) bool { return s.Status == workflow.StatusPending && s.Type != "approval" })
			requireEvents(t, a, 0, eventKind("step.recheck"))
			if got, err := a.Work.Run(a.asEngine(context.Background()), run.ID); err != nil || got.RetryRevision != 0 {
				t.Fatalf("rolled back retry revision: %+v %v", got, err)
			}
			if item := recheckItem(t, a, req); item.TransientRetries != 0 || item.Reason != "" {
				t.Fatalf("rolled back retry budget: %+v", item)
			}
		})
	}
}

func TestLandingRecheckConcurrentRequestsHaveOneBatch(t *testing.T) {
	t.Parallel()
	a, run, _ := landingRecheckFixture(t)
	req, landing := landingRemediationFixture(t, a, run)
	results := race(8, func(int) int {
		result, err := a.Work.RecheckLanding(asPerson("alice"), req)
		if err != nil {
			t.Error(err)
		}
		return len(result.Names)
	})
	if count(results, 3) != 1 || count(results, 0) != 7 {
		t.Fatalf("batches: %v", results)
	}
	requireEvents(t, a, 3, eventKind("step.recheck"))
	if got, err := a.Work.Run(a.asEngine(context.Background()), landing.ID); err != nil || got.RetryRevision != 1 {
		t.Fatalf("one batch revises the run once: %+v %v", got, err)
	}
	if item := recheckItem(t, a, req); item.TransientRetries != 1 {
		t.Fatalf("one batch spends one retry: %+v", item)
	}
}

// The remediation worker's recheck of a landing spends a retry, dispatches
// its gates and starts its cooldown only once Work's recheck commits. A
// recheck whose receipt or commit fails changes none of them, and the item
// can be rechecked again. The failures come first on one AppTown: a failed
// recheck changes nothing, and the cooldown a committed one starts has no
// reset.
func TestLandingRecheckRemediationCallerCountsOnlyCommittedWork(t *testing.T) {
	t.Parallel()
	a, run, _ := landingRecheckFixture(t)
	req, _ := landingRemediationFixture(t, a, run)
	item, err := remediation.ReadItem(context.Background(), a.DB, req.Remediation.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	d := installDispatchFixture(t, a)
	var calls atomic.Int32
	for _, typ := range []string{"scan", "deploy", "tests"} {
		d.runners[typ] = func(task *workflow.Execution) {
			if item := recheckItem(t, a, req); item.TransientRetries != 1 {
				t.Errorf("dispatched before the retry was committed: %+v", item)
			}
			requireEvents(t, a, 3, eventKind("step.recheck"))
			calls.Add(1)
			_, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}
	}
	for _, failure := range []string{"receipt", "commit", "accepted"} {
		t.Run(failure, func(t *testing.T) {
			switch failure {
			case "receipt":
				completionExec(t, a.DB, `ALTER TABLE run_events ADD CONSTRAINT reject_recheck CHECK (kind!='step.recheck')`)
			case "commit":
				completionExec(t, a.DB, `CREATE FUNCTION reject_recheck_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outer commit rejected'; END $$`)
				completionExec(t, a.DB, `CREATE CONSTRAINT TRIGGER reject_recheck_commit AFTER UPDATE ON remediation_items DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_recheck_commit()`)
			}
			a.Fixes.ReconcileItem(context.Background(), item)
			d.wait()
			completionExec(t, a.DB, `ALTER TABLE run_events DROP CONSTRAINT IF EXISTS reject_recheck`)
			completionExec(t, a.DB, `DROP FUNCTION IF EXISTS reject_recheck_commit() CASCADE`)
			wantRetries, wantCalls := 1, int32(3)
			if failure != "accepted" {
				wantRetries, wantCalls = 0, 0
			}
			if item := recheckItem(t, a, req); item.TransientRetries != wantRetries || item.State != remediation.ItemLanding {
				t.Fatalf("retry budget: %+v, want %d retries while landing", item, wantRetries)
			}
			occupancy, err := a.Fixes.Occupancy(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != wantCalls || (occupancy.CooldownUntil == "") != (failure != "accepted") {
				t.Fatalf("calls=%d cooldown=%q", calls.Load(), occupancy.CooldownUntil)
			}
		})
	}
}

func TestLandingRecheckFailedEntryStillBlocksDeployment(t *testing.T) {
	t.Parallel()
	a, run, stepIDs := landingRecheckFixture(t)
	d := installDispatchFixture(t, a)
	var deployed atomic.Bool
	d.runners["deploy"] = func(*workflow.Execution) { deployed.Store(true) }
	for _, typ := range []string{"scan", "tests"} {
		d.runners[typ] = func(task *workflow.Execution) {
			id := task.Step().ID
			_, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			status := workflow.StatusPass
			if id == stepIDs["image"] {
				status = workflow.StatusFail
			}
			if err := task.Finish(workflow.StepOutcome{Status: status}, nil); err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := a.Work.RecheckLanding(asPerson("alice"), workflow.LandingRecheck{ChangeID: run.ChangeID}); err != nil {
		t.Fatal(err)
	}
	d.wait()
	if deployed.Load() {
		t.Fatal("deployment ran after a failed image scan")
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.Name == "deploy" && s.Status == workflow.StatusSkipped })
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.Name == "tests" && s.Status == workflow.StatusPass })
}

func TestLandingRecheckManagedRecoveryHonorsEntryOrder(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "restart"}[restart], func(t *testing.T) {
			a, run, stepIDs := landingRecheckFixture(t)
			if restart {
				if _, err := a.Work.RecheckLanding(asPerson("alice"), workflow.LandingRecheck{Who: "alice", ChangeID: run.ChangeID}); err != nil {
					t.Fatal(err)
				}
				a = restartApp(t, a.DB)
			}
			d := installDispatchFixture(t, a)
			var calls []ids.ID
			for _, typ := range []string{"scan", "deploy", "tests"} {
				d.runners[typ] = func(task *workflow.Execution) {
					id := task.Step().ID
					calls = append(calls, id)
					_, err := task.Claim()
					if err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: []byte(`{"result":"fresh"}`)}, nil); err != nil {
						t.Error(err)
					}
				}
			}
			if restart {
				d.recover()
			} else if _, err := a.Work.RecheckLanding(asPerson("alice"), workflow.LandingRecheck{ChangeID: run.ChangeID}); err != nil {
				t.Fatal(err)
			}
			d.wait()
			if !reflect.DeepEqual(calls, []ids.ID{stepIDs["image"], stepIDs["deploy"], stepIDs["tests"]}) {
				t.Fatalf("entry order / replay: %v", calls)
			}
			requireSteps(t, a, 2, func(s *workflow.Step) bool { return evidenceField(s, "result") == "fresh" && s.Attempt == 2 })
			// Deployment was skipped before it ever claimed an attempt.
			requireSteps(t, a, 1, func(s *workflow.Step) bool {
				return s.Name == "deploy" && evidenceField(s, "result") == "fresh" && s.Attempt == 1
			})
		})
	}
}

// recheckItem is the remediation item a landing recheck spends retries
// from, as its owner reads it.
func recheckItem(t *testing.T, a *App, req workflow.LandingRecheck) remediation.Item {
	t.Helper()
	item, err := remediation.ReadItem(context.Background(), a.DB, req.Remediation.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// A blocked landing's failed gates run again from the change's own route,
// the one apptown recheck <change> uses outside a chat: the failed ones are
// named, the landing is the one answered, and they are dispatched.
//
// Kills: the chat's door as the only one, which left a person at the
// command line with run-step, one step at a time (one-server test, 22 Oct).
func TestAChangesLandingGatesRunAgainFromItsOwnRoute(t *testing.T) {
	t.Parallel()
	a, run, _ := landingRecheckFixture(t)
	d := installDispatchFixture(t, a)
	var calls atomic.Int32
	for _, typ := range []string{"scan", "deploy", "tests"} {
		d.runners[typ] = func(*workflow.Execution) { calls.Add(1) }
	}
	w := authzCall(a.Handler(), "alice", "POST", "/api/v1/changes/"+run.ChangeID.String()+"/recheck", "")
	var got httpapi.RecheckStarted
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("recheck: %d %s", w.Code, w.Body.String())
	}
	if got.RunID != run.ID || !slices.Contains(got.Rerun, "image") || !slices.Contains(got.Rerun, "tests") || slices.Contains(got.Rerun, "passed") {
		t.Fatalf("rechecked %+v, want the failed gates of run %s", got, run.ID)
	}
	d.wait()
	if calls.Load() == 0 {
		t.Fatal("the rechecked gates were not dispatched")
	}
}
