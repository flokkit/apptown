package forge

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workcommands"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/forge/conformance"
)

// Remediation (design/subsystems/remediation.md): scope resolution, the
// declared worker pool, one Change per project at a time, the item's life
// driven from the tables, stuck with a reason, rate-limit back-off, and fixes
// as records written by the review.

// Resolve and dispatch are bound to this App; the production store still
// commits change, run and attempt together without launching real agents.
func stubRemediationStart(t *testing.T, a *App, fail error) *[]ids.ID {
	return stubRemediationStartWithSpec(t, a, fail, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build"}}})
}

func stubRemediationStartWithSpec(t *testing.T, a *App, fail error, spec schema.Spec) *[]ids.ID {
	t.Helper()
	var opened []ids.ID
	installFakeGit(t, a, nil)
	rebuildFixes(t, a, func(d *remediation.Dependencies) {
		d.Resolve = func(_ context.Context, it remediation.Item, _ remediation.VerificationScope) (remediation.StartDefinition, error) {
			if fail != nil {
				return remediation.StartDefinition{}, fail
			}
			if _, err := a.conformance.AdoptedCheck(testConformanceContext("t"), it.ProjectID, it.Block, it.Check); err != nil {
				return remediation.StartDefinition{Skip: "the project no longer adopts this check"}, nil
			}
			req := workflow.StartRequest{ProjectID: it.ProjectID, WorkflowName: "implement"}
			source, err := a.Work.StartSource(a.asForge(context.Background(), it.ProjectID, "test"), req)
			if err != nil {
				return remediation.StartDefinition{}, err
			}
			prep := workcommands.NewPreparation(a.DB, definedWorkflows{Service: a.Adoption, definitions: []workflow.PreparedRun{{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: spec}}}, a.branches, a.cfg.dataDir, holderPresenceFor(a), a.chatSaves, nil)
			run, err := prep.Resolve(context.Background(), req, source)
			body := "fix " + it.Check
			if it.Reason != "" {
				body += "\nPrevious attempt: " + it.Reason
			}
			return remediation.StartDefinition{Run: run, Title: "fix " + it.Check, Body: body}, err
		}
		d.Work = fakeWorkDispatch{Work: d.Work, dispatch: func(runID ids.ID) error {
			snapshot, err := a.Work.Snapshot(a.asEngine(context.Background()), runID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			opened = append(opened, snapshot.Run.ChangeID)
			err = a.Work.DispatchStarted(runID)
			a.Work.Wait()
			return err
		}}
	})
	return &opened
}

// stubVerify answers the post-landing review with a fixed state.
func stubVerify(t *testing.T, a *App, state conformance.ObservationState) {
	t.Helper()
	rebuildFixes(t, a, func(d *remediation.Dependencies) {
		d.Review = func(context.Context, remediation.Item, remediation.VerificationScope) remediation.Review {
			return remediation.Review{ID: idstest.N(99), State: state, Detail: "stubbed"}
		}
	})
}

// enableImplementLane records a real selected and pinned workflow for each
// fixture project, so remediation uses production eligibility rules.
func enableImplementLane(t *testing.T, a *App) {
	t.Helper()
	const yaml = `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: implement}
spec:
  kind: pipeline
  role: working
  stages:
    - name: build
      steps:
        - {name: work, uses: script, with: {run: "true"}}
`
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), yaml, "t"); err != nil || name != "implement" || version != 1 {
		t.Fatalf("publish implement workflow: %s@%d %v", name, version, err)
	}
	projects, err := a.Projects.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if err := a.lifecycle.SelectLane(t.Context(), project.ID, "implement", true, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
}

func failingFixture(t *testing.T, a *App) (ids.ID, ids.ID) {
	t.Helper()
	testAdministrator(t, a, "t")
	tgLocalSh(t, a)
	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "// FIXME"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high"), shCheck("no-todo", `if grep -rl TODO . >/dev/null 2>&1; then echo "TODO"; exit 1; fi`, "note")))
	beta, _, _ := fleetRepo(t, a, "beta", map[string]string{"b.txt": "// FIXME"})
	adopt(t, a, beta, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	return alpha, beta
}

func itemStates(t *testing.T, a *App, remID ids.ID) map[string]string {
	out := map[string]string{}
	for _, it := range remediationItemsForTest(t, a, remID) {
		out[it.ProjectSlug+"/"+it.Check] = it.State
	}
	return out
}

func TestRemediationScopeAndStart(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	alpha, _ := failingFixture(t, a)
	// alpha has a TODO? no — only FIXME fails; no-todo conforms. Scope = 2 items.
	findings, err := a.Fixes.Scope(testConformanceContext("t"), "all", "")
	if err != nil || len(findings) != 2 {
		t.Fatalf("scope all should be the two FIXME findings: %v %+v", err, findings)
	}
	if f, _ := a.Fixes.Scope(testConformanceContext("t"), "check", "hygiene@1·no-fixme"); len(f) != 2 {
		t.Fatalf("check scope: %d", len(f))
	}
	if f, _ := a.Fixes.Scope(testConformanceContext("t"), "project", "beta"); len(f) != 1 || f[0].ProjectSlug != "beta" {
		t.Fatalf("project scope: %+v", f)
	}
	m, items, err := a.Fixes.Start(testConformanceContext("t"), "all", "", "bls", 0)
	if err != nil || len(items) != 2 || m.Status != remediation.RemRunning {
		t.Fatalf("start: %v %+v", err, m)
	}
	// Frozen provenance on the item.
	if items[0].OriginVerdictID.IsZero() || items[0].OriginSHA == "" || items[0].OriginEvaluatorVer == "" {
		t.Fatalf("items must freeze the verdict they fix: %+v", items[0])
	}
	if _, _, err := a.Fixes.Start(testConformanceContext("t"), "project", "nope", "bls", 0); err == nil {
		t.Fatal("an empty scope must be refused")
	}
	_ = alpha
}

func TestWorkerPoolAndOneChangePerProject(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	failingFixture(t, a)
	enableImplementLane(t, a)
	opened := stubRemediationStart(t, a, nil)
	if a.Fixes.WorkerCount(context.Background(), remediation.WorkerKindRemediation) != 2 {
		t.Fatalf("default pool is 2, got %d", a.Fixes.WorkerCount(context.Background(), remediation.WorkerKindRemediation))
	}
	if err := a.Fixes.SetWorkerCount(context.Background(), remediation.WorkerKindRemediation, 1, "bls"); err != nil {
		t.Fatal(err)
	}
	m, _, err := a.Fixes.Start(testConformanceContext("t"), "all", "", "bls", 0)
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	if len(*opened) != 1 {
		t.Fatalf("one worker → one item started, got %d", len(*opened))
	}
	st := itemStates(t, a, m.ID)
	working, queued := 0, 0
	for _, s := range st {
		switch s {
		case remediation.ItemWorking:
			working++
		case remediation.ItemQueued:
			queued++
		}
	}
	if working != 1 || queued != 1 {
		t.Fatalf("expected 1 working + 1 queued, got %v", st)
	}
	occ := remediationOccupancyForTest(t, a)
	if occ.Workers != 1 || occ.Busy != 1 || occ.Queued != 1 {
		t.Fatalf("occupancy: %+v", occ)
	}
	// A second tick with the pool full starts nothing more.
	a.Fixes.Pass(context.Background())
	if len(*opened) != 1 {
		t.Fatalf("pool full → nothing more starts, got %d", len(*opened))
	}
	// Two workers, but the second item is on the OTHER project — it starts;
	// a third item on a busy project would not. Simulate: bump the pool.
	_ = a.Fixes.SetWorkerCount(context.Background(), remediation.WorkerKindRemediation, 2, "bls")
	a.Fixes.Pass(context.Background())
	if len(*opened) != 2 {
		t.Fatalf("second worker picks the other project, got %d", len(*opened))
	}
}

// A remediation pass is a queue job: a person's action asks for one
// "wake" pass, which the queue coalesces while it waits, and running that
// job runs one pass, which starts the queued item.
//
// Kills: Wake asking for nothing (the person waits for the schedule);
// repeated wakes piling up passes; a pass job that does not run the pass.
func TestARemediationPassRunsAsAQueueJob(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	failingFixture(t, a)
	enableImplementLane(t, a)
	stubRemediationStartWithSpec(t, a, nil, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	m, _, err := a.Fixes.Start(testConformanceContext("t"), "project", "beta", "bls", 0)
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.Wake(context.Background())
	a.Fixes.Wake(context.Background())
	queued := 0
	for _, j := range operationJobs(t, a, remediation.PassJob) {
		if j.Key == passWakeKey && j.State == "available" {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("two wakes queued %d passes", queued)
	}
	runQueuedJobForTest(t, a, remediation.PassJob, passWakeKey)
	if it := remediationItemsForTest(t, a, m.ID)[0]; it.State != remediation.ItemWorking || it.Attempt != 1 {
		t.Fatalf("the pass job started the queued item: %+v", it)
	}
}

func TestRemediationItemFollowsWorkAndLanding(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	failingFixture(t, a)
	enableImplementLane(t, a)
	stubRemediationStartWithSpec(t, a, nil, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	stubVerify(t, a, conformance.Conforms)
	m, _, err := a.Fixes.Start(testConformanceContext("t"), "project", "beta", "bls", 0)
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	it := remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemWorking || it.ChangeID.IsZero() || it.RunID.IsZero() || it.Attempt != 1 || it.LeaseWorker == "" {
		t.Fatalf("after the first tick the item is working with a lease: %+v", it)
	}
	a.Fixes.Pass(context.Background())
	if it = remediationItemsForTest(t, a, m.ID)[0]; it.State != remediation.ItemAccepting {
		t.Fatalf("the declared pending approval means accepting: %+v", it)
	}
	work, err := a.Work.Snapshot(asPerson("t"), it.RunID, workflow.DetailView)
	if err != nil || len(work.Steps) != 1 {
		t.Fatalf("declared remediation approval: %+v %v", work, err)
	}
	if _, err := a.Work.Approve(asPerson("t"), workflow.Approval{StepID: work.Steps[0].ID, Decision: workflow.VoteApprove}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "remediation-land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
	landing, err := a.Work.StartRun(asPerson("t"), workflow.StartChangeRun{ChangeID: it.ChangeID, Workflow: "remediation-land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	if it = remediationItemsForTest(t, a, m.ID)[0]; it.State != remediation.ItemLanding {
		t.Fatalf("change landing → item landing: %+v", it)
	}
	land, err := a.Work.Snapshot(asPerson("t"), landing.ID, workflow.DetailView)
	if err != nil || len(land.Steps) != 1 {
		t.Fatalf("declared landing approval: %+v %v", land, err)
	}
	if _, err := a.Work.Approve(asPerson("t"), workflow.Approval{StepID: land.Steps[0].ID, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("t"), land.Steps[0].ID)}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	a.Fixes.Pass(context.Background())
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemDone || it.VerifiedReviewID != idstest.N(99) {
		t.Fatalf("shipped → verified by the review → done: %+v", it)
	}
	rem, _ := a.Fixes.Remediation(context.Background(), m.ID)
	if rem.Status != remediation.RemCompleted {
		t.Fatalf("job completes when nothing is left: %+v", rem)
	}
}

func TestStuckReasonsAndRateLimitBackoff(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	failingFixture(t, a)
	enableImplementLane(t, a)
	d := installDispatchFixture(t, a)
	var execution atomic.Int32
	d.runners["script"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		outcome := workflow.StepOutcome{Status: workflow.StatusError, Evidence: json.RawMessage(`{"reason":"the model provider refused: 429 Too Many Requests"}`)}
		if execution.Add(1) == 2 {
			outcome = workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"3 tests failed"}`)}
		}
		if err := task.Finish(outcome, nil); err != nil {
			t.Error(err)
		}
	}
	stubRemediationStartWithSpec(t, a, nil, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "repair", Uses: "script"}}}}})
	m, _, err := a.Fixes.Start(testConformanceContext("t"), "project", "beta", "bls", 0)
	if err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	it := remediationItemsForTest(t, a, m.ID)[0]
	// An abandoned run carries a provider rate-limit failure; the pool backs off.
	if _, err := a.Work.Abandon(asPerson("t"), workflow.StopWork{RunID: it.RunID, Reason: "the model provider refused"}); err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemQueued || !it.NotBefore.Valid || it.TransientRetries != 1 || !strings.Contains(it.Reason, "backing off") {
		t.Fatalf("a rate limit re-queues with a backoff: %+v", it)
	}
	if occ := remediationOccupancyForTest(t, a); occ.CooldownUntil == "" {
		t.Fatalf("the pool should be cooling down: %+v", occ)
	}
	// While cooling / before not_before, nothing starts.
	a.Fixes.Pass(context.Background())
	if it = remediationItemsForTest(t, a, m.ID)[0]; it.State != remediation.ItemQueued {
		t.Fatalf("nothing starts during the cooldown: %+v", it)
	}
	// Clear the cooldown and the backoff: a fresh attempt supersedes the old change.
	// A rebuilt service has a fresh worker, and so no cooldown.
	rebuildFixes(t, a)
	// sql-guarantee: Advance only the queued item backoff clock while retaining its rate-limit history; RetryItem requires a stuck item and would change the scenario, while RequeueItem is owner-private.
	_, _ = a.DB.Exec(`UPDATE remediation_items SET not_before=NULL WHERE id=?`, it.ID)
	oldChange := it.ChangeID
	a.Fixes.Pass(context.Background())
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemWorking || it.Attempt != 2 || it.ChangeID == oldChange {
		t.Fatalf("a fresh attempt on a new change: %+v", it)
	}
	previous, err := a.Work.Change(asPerson("t"), oldChange)
	if err != nil {
		t.Fatal(err)
	}
	if previous.State != workflow.ChangeAbandoned || !strings.Contains(previous.Summary, "superseded") {
		t.Fatalf("the old change is superseded, never landable: %s %q", previous.State, previous.Summary)
	}
	// A plain failure sticks with the gate's words; retry needs a person.
	if _, err := a.Work.Abandon(asPerson("t"), workflow.StopWork{RunID: it.RunID, Reason: "tests failed"}); err != nil {
		t.Fatal(err)
	}
	a.Fixes.Pass(context.Background())
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemStuck || !strings.Contains(it.Reason, "3 tests failed") {
		t.Fatalf("a real failure is stuck with the reason: %+v", it)
	}
	rem, _ := a.Fixes.Remediation(context.Background(), m.ID)
	if rem.Status != remediation.RemCompletedWithStuck {
		t.Fatalf("a job with only stuck items is completed_with_stuck: %+v", rem)
	}
	if err := a.Fixes.RetryItem(context.Background(), it.RemediationID, it.ID, "bls", "try using the helper"); err != nil {
		t.Fatal(err)
	}
	rem, _ = a.Fixes.Remediation(context.Background(), m.ID)
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemQueued || rem.Status != remediation.RemRunning || !strings.Contains(it.Reason, "try using the helper") {
		t.Fatalf("retry re-queues and reopens the job: %+v %+v", it, rem)
	}
	if err := a.Fixes.SkipItem(context.Background(), it.RemediationID, it.ID, "bls", ""); err == nil {
		t.Fatal("skip needs a reason")
	}
	if err := a.Fixes.SkipItem(context.Background(), it.RemediationID, it.ID, "bls", "not worth it"); err != nil {
		t.Fatal(err)
	}
	if err := a.Fixes.Cancel(context.Background(), m.ID, "bls"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectLeaseRespectsOrdinaryChanges(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	_, beta := failingFixture(t, a)
	enableImplementLane(t, a)
	opened := stubRemediationStart(t, a, nil)
	// An ordinary change is landing on beta.
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "ordinary-land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
	change, err := a.Work.Create(asPerson("t"), workflow.ChangeDraft{ProjectID: beta, Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	landing, err := a.Work.StartRun(asPerson("t"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "ordinary-land"})
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := a.Fixes.Start(testConformanceContext("t"), "project", "beta", "bls", 0)
	a.Fixes.Pass(context.Background())
	if len(*opened) != 0 {
		t.Fatal("a landing change makes the project busy — nothing starts")
	}
	snapshot, err := a.Work.Snapshot(asPerson("t"), landing.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("landing approval: %+v %v", snapshot, err)
	}
	if _, err := a.Work.Approve(asPerson("t"), workflow.Approval{StepID: snapshot.Steps[0].ID, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("t"), snapshot.Steps[0].ID)}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	landing, err = workflow.ReadRunIn(context.Background(), a.DB, landing.ID)
	shipped, readErr := a.changeByID(context.Background(), change.ID)
	if err != nil || readErr != nil || landing.Status != workflow.RunDone || shipped.State != workflow.ChangeShipped {
		t.Fatalf("ordinary landing did not settle: run=%+v change=%+v errors=%v/%v", landing, shipped, err, readErr)
	}
	a.Fixes.Pass(context.Background())
	if len(*opened) != 1 {
		t.Fatalf("once free the item starts, got %d", len(*opened))
	}
	_ = m
}

func TestBlockedLandingGoesStuck(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	failingFixture(t, a)
	enableImplementLane(t, a)
	d := installDispatchFixture(t, a)
	d.runners["scan"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail, Evidence: json.RawMessage(`{"reason":"19 actionable finding(s)"}`)}, nil); err != nil {
			t.Error(err)
		}
	}
	stubRemediationStart(t, a, nil)
	m, _, _ := a.Fixes.Start(testConformanceContext("t"), "project", "beta", "bls", 0)
	a.Fixes.Pass(context.Background())
	it := remediationItemsForTest(t, a, m.ID)[0]
	if result, err := a.Work.Advance(asPerson("t"), it.RunID); err != nil || !result.Advanced {
		t.Fatalf("complete remediation work: %+v %v", result, err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "ship", Steps: []schema.Step{{Name: "scan-image:trivy", Uses: "scan"}}}}}})
	landing, err := a.Work.StartRun(asPerson("t"), workflow.StartChangeRun{ChangeID: it.ChangeID, Workflow: "land"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	landing, err = workflow.ReadRunIn(context.Background(), a.DB, landing.ID)
	if err != nil || landing.Status != workflow.RunRunning {
		t.Fatalf("failed gate must keep landing active: %+v %v", landing, err)
	}
	a.Fixes.Pass(context.Background()) // → landing
	a.Fixes.Pass(context.Background()) // → stuck
	a.Fixes.Pass(context.Background()) // observe the latest landing after the phase commit
	it = remediationItemsForTest(t, a, m.ID)[0]
	if it.State != remediation.ItemStuck || !strings.Contains(it.Reason, "blocked") || !strings.Contains(it.Reason, "19 actionable") {
		t.Fatalf("a blocked landing is stuck with the gate's words: %+v", it)
	}
}

func remediationItemsForTest(t *testing.T, a *App, id ids.ID) []remediation.Item {
	t.Helper()
	items, err := a.Fixes.Items(context.Background(), &id)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func remediationOccupancyForTest(t *testing.T, a *App) remediation.Occupancy {
	t.Helper()
	occupancy, err := a.Fixes.Occupancy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return occupancy
}
