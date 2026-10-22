package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

func startupFixture(t *testing.T, withChild ...bool) (*database.DB, ids.ID, ids.ID) {
	t.Helper()
	a := testApp(t)
	projectID := createReadTestProject(t, a, "restart")
	ctx := a.asEngine(context.Background())
	steps := []schema.Step{{Name: "work", Uses: "script", With: map[string]any{"script": "true"}}, {Name: "finished", Uses: "script", With: map[string]any{"script": "true"}}}
	if len(withChild) > 0 && withChild[0] {
		steps = append(steps, schema.Step{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "child"}})
	}
	installFakeGit(t, a, nil)
	defineWorkflows(t, a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "test", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: steps}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "wait", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}},
	)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if task.Step().Name == "finished" {
				if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(`{"kept":true}`)}, nil); err != nil {
					t.Error(err)
				}
			}
			// The other external worker was interrupted after claiming its step.
		}}
	})
	a.Work.Stop()
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Title: "restart work"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type == "workflow/call" {
			if _, err := a.Work.Call(ctx, workflow.WorkflowCall{ParentStepID: step.ID, Workflow: "child"}); !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
		} else if err := a.Work.ExecuteStep(ctx, step.ID); err != nil {
			t.Fatal(err)
		}
	}
	db := a.DB
	completed, env := startupRelease(t, db, projectID, "main")
	outcomes := deployment.NewOutcomes(db, nil)
	attempt, _, err := outcomes.Admit(ctx, deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", completed.ID), Inputs: deployment.Inputs{ReleaseID: completed.ID, Revision: "completed"}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := outcomes.Complete(ctx, deployment.Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy"}}); err != nil || result != deployment.Applied {
		t.Fatalf("complete release: %s %v", result, err)
	}
	// A release admitted before the process stopped, with no durable owner yet.
	startupRelease(t, db, projectID, "main")
	testAdministrator(t, a, "alice")
	reviewCtx := testConformanceContext("alice")
	for _, done := range []bool{false, true} {
		review, err := a.conformance.BeginReview(reviewCtx, conformance.ReviewRequest{Trigger: "manual", Projects: []ids.ID{projectID}})
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if _, err := review.Finish(reviewCtx, conformance.Completion{Projects: 1, ProjectsDone: 1}, conformance.ReviewDone); err != nil {
				t.Fatal(err)
			}
		} else if err := review.Progress(reviewCtx, 0, "restart"); err != nil {
			t.Fatal(err)
		}
	}
	if err := (backupRequests{db: db}).RequestBackup(context.Background(), "restart/main", "startup fixture"); err != nil {
		t.Fatal(err)
	}
	// Claim through River's real transition. The Jobs subprocess test establishes
	// process death; this composition test checks rollback across all participants.
	claimAvailableJobs(t, db, "dead-startup-fixture", 1)
	return db, projectID, run.ID
}

// claimAvailableJobs claims every available job in River's default queue as
// owner, by River's own transition, and requires exactly want of them. A
// claim that finds fewer is asked again, within a bound. Why a committed,
// available job was once answered as none (twice, under a loaded gate) is
// not established: the hypothesis is that another transaction held its row
// locked, which River's claim skips. A claim that never finds them says
// what the queue holds, so the next sighting can settle it.
func claimAvailableJobs(t *testing.T, db *database.DB, owner string, want int) {
	t.Helper()
	var claimed []*rivertype.JobRow
	var err error
	got := 0
	if !poll.Until(t.Context(), 30*time.Second, 50*time.Millisecond, func() bool {
		claimed, err = riverdatabasesql.New(db.Pool()).GetExecutor().JobGetAvailable(context.Background(), &riverdriver.JobGetAvailableParams{ClientID: owner, MaxAttemptedBy: 100, MaxToLock: 100, Queue: river.QueueDefault})
		got += len(claimed)
		return err != nil || got >= want
	}) || err != nil || got != want {
		// What the queue holds, read by its owner.
		held, lerr := jobs.List(context.Background(), db, 100)
		t.Fatalf("claimed %d of %d jobs as %s (%v); the queue holds %+v (%v)", got, want, owner, err, held, lerr)
	}
}

func startupRelease(t *testing.T, db *database.DB, projectID ids.ID, name string) (deployment.NumberedRelease, *deployment.Deployment) {
	t.Helper()
	target, err := deployment.NewTargets(db, nil).Put(context.Background(), &deployment.Target{Name: "startup", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	env, err := deployment.NewDeployments(db).EnsureStanding(t.Context(), projectID, name, target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	release, err := deployment.NewReleases(db).Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: projectID, DeploymentID: env.ID, Deployment: name, Actor: "alice"}, "startup fixture")
	if err != nil {
		t.Fatal(err)
	}
	return release, env
}

func startupSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: a failed or repeated repair must leave every step, release, release event and review byte-identical, timestamps included; only the whole tables, read independently of the repair, show that.
	if err := db.QueryRow(`SELECT jsonb_build_object(
		'steps',(SELECT jsonb_agg(s ORDER BY id) FROM steps s),
		'releases',(SELECT jsonb_agg(r ORDER BY id) FROM releases r),
		'events',(SELECT jsonb_agg(e ORDER BY id) FROM release_events e),
		'reviews',(SELECT jsonb_agg(r ORDER BY id) FROM conformance_reviews r))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	// Read full queue rows through River, independently of AppTown's repair query.
	client, err := river.NewClient(riverdatabasesql.New(db.Pool()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.JobList(context.Background(), river.NewJobListParams())
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "jobs.interruption-settled"})
	if err != nil {
		t.Fatal(err)
	}
	queueSnapshot, err := json.Marshal(struct {
		Jobs     []*rivertype.JobRow
		Receipts []audit.Entry
	}{rows.Jobs, receipts})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot + string(queueSnapshot)
}

func TestNewDoesNotPerformStartupRecoveryOrInstallGlobalHooks(t *testing.T) {
	// Not parallel: it replaces the package-wide selfPanicReporter.
	db, _, _ := startupFixture(t)
	before := startupSnapshot(t, db)
	previous := selfPanicReporter
	defer func() { selfPanicReporter = previous }()
	var called bool
	selfPanicReporter = func(string, any, []byte) { called = true }
	dir := filepath.Join(t.TempDir(), "not-created")
	a := constructApp(t, db, Config{DataDir: dir})
	if a.DB != db || a.cfg.dataDir != dir || startupSnapshot(t, db) != before {
		t.Fatal("construction repaired database state")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("construction touched the filesystem: %v", err)
	}
	selfPanicReporter("test", nil, nil)
	if !called {
		t.Fatal("construction replaced the process's panic reporter")
	}
}

func TestStartupRepairRollsBackAndCanBeRetried(t *testing.T) {
	t.Parallel()
	// sql-guarantee: each entry names a production statement to fail; none is a read.
	for _, failure := range []string{"UPDATE steps", "-- name: SettleInterruptedOperation", "INSERT INTO audit_log", "release history", "UPDATE conformance_reviews", "SELECT COUNT(*) FROM steps"} {
		t.Run(failure, func(t *testing.T) {
			db, projectID, runID := startupFixture(t)
			before := startupSnapshot(t, db)
			a := constructApp(t, db, Config{DataDir: t.TempDir()})
			if failure == "release history" {
				// Fail inside the CTE after its release update, not before it.
				completionExec(t, db, `ALTER TABLE release_events ADD CONSTRAINT reject_restart CHECK (text NOT LIKE '%restarted mid-release%')`)
			} else {
				dbtest.FailNextStatement(t, a.DB, failure)
			}
			report, err := (startupRecoveryStore{db: db}).recover(t.Context(), a.jobs.Client.ID())
			if err == nil || report != (startupRepairReport{}) {
				t.Fatalf("failed repair returned success or uncommitted counts: report=%+v error=%v", report, err)
			}
			if after := startupSnapshot(t, db); after != before {
				t.Fatal("failed repair left partial state or history")
			}
			if failure == "release history" {
				completionExec(t, db, `ALTER TABLE release_events DROP CONSTRAINT reject_restart`)
			}
			if err := a.RecoverInterruptedWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := readStartupState(t, a, projectID, runID)
			if got.orphaned != 1 || got.failedReleases != 1 || got.restartEvents != 1 || got.events != 4 || got.endedReviews != 1 {
				t.Fatalf("retried repair: %+v", got)
			}
		})
	}
}

func TestStartupRepairIsRepeatableAndKeepsCompletedWork(t *testing.T) {
	t.Parallel()
	db, projectID, runID := startupFixture(t)
	a := constructApp(t, db, Config{DataDir: t.TempDir()})
	reports := make([]startupRepairReport, 5)
	results := race(5, func(i int) int {
		var err error
		reports[i], err = (startupRecoveryStore{db: db}).recover(t.Context(), a.jobs.Client.ID())
		if err != nil {
			t.Error(err)
			return 0
		}
		return 1
	})
	if count(results, 1) != 5 {
		t.Fatal("concurrent repairs did not all complete")
	}
	var total startupRepairReport
	for _, report := range reports {
		total.jobs.Settled += report.jobs.Settled
		total.jobs.Cancelled += report.jobs.Cancelled
		total.jobs.Available += report.jobs.Available
		total.steps += report.steps
		total.releases += report.releases
		total.reviews += report.reviews
	}
	if total != (startupRepairReport{jobs: jobs.InterruptionRepair{Settled: 1, Cancelled: 1}, steps: 1, releases: 1, reviews: 1}) {
		t.Fatalf("concurrent repair counts duplicated work: %+v", total)
	}
	got := readStartupState(t, a, projectID, runID)
	if got.restartEvents != 1 || got.events != 4 || got.kept != 1 || got.releasedReleases != 1 || got.finishedReviews != 1 {
		t.Fatalf("concurrent repair lost completed work or duplicated history: %+v", got)
	}
	before := startupSnapshot(t, db)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if startupSnapshot(t, db) != before {
		t.Fatal("repeated repair changed timestamps or duplicated history")
	}
}

func TestStartupRepairRefusesVerifyOnlyMode(t *testing.T) {
	t.Parallel()
	db, _, _ := startupFixture(t)
	before := startupSnapshot(t, db)
	a := constructApp(t, db, Config{DataDir: t.TempDir(), VerifyOnly: true})
	normal := constructApp(t, db, Config{DataDir: t.TempDir()})
	if err := a.RecoverInterruptedWork(t.Context()); err == nil {
		t.Fatal("verify-only mode allowed restart repair")
	}
	if startupSnapshot(t, db) != before {
		t.Fatal("verify-only startup changed records")
	}
	if err := normal.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("verify-only instance prevented normal instance recovery: %v", err)
	}
	if err := a.RecoverInterruptedWork(t.Context()); err == nil {
		t.Fatal("normal recovery changed the verify-only instance's policy")
	}
}

func TestStartupRepairLogsCommittedCountsAndPreservedWork(t *testing.T) {
	// Not parallel: it captures the process-wide standard logger's output.
	db, project, _ := startupFixture(t, true)
	release, env := startupRelease(t, db, project, "owned")
	if _, _, err := deployment.NewOutcomes(db, nil).Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", release.ID), Inputs: deployment.Inputs{ReleaseID: release.ID, Revision: "owned"}}); err != nil {
		t.Fatal(err)
	}

	// A file also safely captures any incidental background log writes.
	output, err := os.CreateTemp(t.TempDir(), "startup-log")
	if err != nil {
		t.Fatal(err)
	}
	previous := log.Writer()
	log.SetOutput(output)
	t.Cleanup(func() { log.SetOutput(previous); _ = output.Close() })
	readLog := func() string {
		t.Helper()
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	a := constructApp(t, db, Config{DataDir: t.TempDir()})
	dbtest.FailNextStatement(t, a.DB, "UPDATE conformance_reviews")
	if err := a.RecoverInterruptedWork(t.Context()); err == nil {
		t.Fatal("repair unexpectedly succeeded")
	}
	failedLog := readLog()
	if !strings.Contains(failedLog, "startup repair: starting") || !strings.Contains(failedLog, "startup repair: failed after") || strings.Contains(failedLog, "startup repair: committed") {
		t.Fatalf("failed repair log claimed success or omitted failure: %s", failedLog)
	}
	for _, repaired := range []int{1, 0} {
		before := len(readLog())
		if err := a.RecoverInterruptedWork(t.Context()); err != nil {
			t.Fatal(err)
		}
		entry := readLog()[before:]
		for _, want := range []string{
			"startup repair: starting", "startup repair: committed in",
			fmt.Sprintf("interrupted steps marked for recovery=%d", repaired),
			fmt.Sprintf("orphaned releases failed=%d", repaired),
			fmt.Sprintf("interrupted reviews ended=%d", repaired),
			fmt.Sprintf("river rows settled=%d", repaired), "preserved child workflow calls=1", "releases owned by queued jobs or deployment attempts=1",
		} {
			if !strings.Contains(entry, want) {
				t.Errorf("missing %q in startup log: %s", want, entry)
			}
		}
	}
}

// A queued release job whose key names no release does not stop startup:
// it protects no release, and the repair goes on without it.
func TestStartupRepairIgnoresAQueuedReleaseThatNamesNoRelease(t *testing.T) {
	t.Parallel()
	db, _, _ := startupFixture(t)
	a := constructApp(t, db, Config{DataDir: t.TempDir()})
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(context.Background(), tx, jobs.Request{Operation: deployment.ReleaseJob, Key: "not-a-release"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatalf("one queued release job that names no release stopped startup: %v", err)
	}
}

// startupState is what the fixture's owners report after a repair.
type startupState struct {
	orphaned, kept                   int // the run's steps ended by the restart, and kept as they finished
	failedReleases, releasedReleases int // released ones count only with a release time
	events, restartEvents            int // every release's history lines, and those naming the restart
	endedReviews, finishedReviews    int // reviews ended in error with no project in hand, and reviews done
}

// readStartupState reads the fixture's one run, its project's releases and
// the fleet's reviews through their owners. The fixture holds nothing else.
func readStartupState(t *testing.T, a *App, projectID, runID ids.ID) startupState {
	t.Helper()
	var out startupState
	steps, err := a.Work.RunSteps(a.asEngine(context.Background()), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Status == workflow.StatusError && step.FinishedAt.Valid && stepReason(t, step) == "orphaned by apptownd restart" {
			out.orphaned++
		}
		// The old predicate was evidence_json @> '{"kept":true}': the exact key
		// holding JSON true, not a string or another spelling.
		if kept, ok := exactKey(step.Evidence, "kept"); step.Status == workflow.StatusPass && ok && string(kept) == "true" {
			out.kept++
		}
	}
	releases, err := a.Deployment.Releases.ForProject(context.Background(), projectID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range releases {
		switch {
		case release.State == deployment.ReleaseFailed:
			out.failedReleases++
		case release.State == deployment.Released && release.ReleasedAt.Valid:
			out.releasedReleases++
		}
		events, err := a.Deployment.Releases.Events(context.Background(), release.ID)
		if err != nil {
			t.Fatal(err)
		}
		out.events += len(events)
		for _, event := range events {
			if strings.Contains(event.Text, "restarted mid-release") {
				out.restartEvents++
			}
		}
	}
	reviews, err := a.conformance.Reviews(testConformanceContext("alice"), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, review := range reviews {
		switch {
		case review.Status == conformance.ReviewError && review.FinishedAt.Valid && review.CurrentProject == "":
			out.endedReviews++
		case review.Status == conformance.ReviewDone && review.FinishedAt.Valid:
			out.finishedReviews++
		}
	}
	return out
}

// A what-if found queued or running when AppTown starts was interrupted
// by the restart: the repair settles it as interrupted, keeping the rows it
// recorded, and leaves a finished one as it was. It is not resumed, and
// the job that later delivers a queued one runs nothing.
//
// Kills: a what-if left "running" forever after a restart; a queued one
// started after the restart; a finished what-if rewritten by the repair.
func TestStartupRepairSettlesAnUnfinishedWhatIfAsInterrupted(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	ctx := testConformanceContext("boss")
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	game, err := a.Projects.BySlug(ctx, "game")
	if err != nil {
		t.Fatal(err)
	}
	in := conformance.WhatIfRequest{Pack: "team", Version: 1, Projects: []ids.ID{game.ID}}
	admit := func() ids.ID {
		id, err := fleetReviewsFor(a).StartWhatIf(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	claim := func(id ids.ID) *conformance.WhatIfRun {
		run, _, err := a.conformance.ClaimWhatIf(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	queued, runningID, finishedID := admit(), admit(), admit()
	if err := claim(runningID).Record(ctx, []conformance.WhatIfRow{{ProjectID: game.ID, ProjectSlug: "game", Result: conformance.WhatIfUnchanged, Block: "b", Check: "c"}}, conformance.WhatIfSpend{}); err != nil {
		t.Fatal(err)
	}
	if err := claim(finishedID).Finish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverInterruptedWork(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := a.conformance.WhatIf(ctx, runningID)
	if err != nil || got.State != conformance.WhatIfInterrupted || !strings.Contains(got.Detail, "restarted") || len(got.Rows) != 1 || !got.FinishedAt.Valid {
		t.Fatalf("a what-if running at startup: %+v %v", got, err)
	}
	runQueuedJobForTest(t, a, verification.WhatIfJob, fmt.Sprint(queued))
	got, err = a.conformance.WhatIf(ctx, queued)
	if err != nil || got.State != conformance.WhatIfInterrupted || len(got.Rows) != 0 || got.ProjectsTotal != 0 {
		t.Fatalf("a what-if queued at startup: %+v %v", got, err)
	}
	if got, err := a.conformance.WhatIf(ctx, finishedID); err != nil || got.State != conformance.WhatIfDone {
		t.Fatalf("a finished what-if: %+v %v", got, err)
	}
}

// A review queued when AppTown starts ends with the running ones, and
// the job that later delivers it runs nothing: nothing starts after a
// restart. Kills: a repair that ends only running reviews.
func TestStartupRepairEndsAQueuedReview(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	ctx := testConformanceContext("boss")
	game, err := a.Projects.BySlug(ctx, "game")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := fleetReviewsFor(a).StartFleetReviewWith(ctx, "manual", conformance.ReviewOptions{Projects: []ids.ID{game.ID}})
	if err != nil || rev.Status != conformance.ReviewQueued {
		t.Fatalf("admitted review: %+v %v", rev, err)
	}
	if err := a.RecoverInterruptedWork(ctx); err != nil {
		t.Fatal(err)
	}
	runQueuedJobForTest(t, a, verification.ReviewJob, fmt.Sprint(rev.ID))
	got, err := savedReview(a, rev.ID)
	if err != nil || got.Status != conformance.ReviewError || !got.FinishedAt.Valid || got.ProjectsTotal != 0 {
		t.Fatalf("a review queued at startup: %+v %v", got, err)
	}
}

// AppTown's own model calls go through the gateway on every instance, so
// startup repair closes the calls an old process sent on every instance,
// including one without the runtime inference listener: the spend line is
// locked in as unknown at once, before its own time would run out. (That
// MarkInterrupted also closes the receipt is the usage owner's test.)
//
// Kills: recovery of model calls run only when the listener is enabled.
func TestStartupRepairClosesModelCallsWithoutTheInferenceListener(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	old := newApp(t, db)
	if old.cfg.inferenceURL != "" {
		t.Fatal("the fixture has an inference listener")
	}
	pid := createReadTestProject(t, old, "shop")
	receipt, err := old.Usage.Requests.OpenCall(context.Background(), usage.Request{Kind: "forge", Actor: "apptown-system", ProjectID: pid, Provider: "native", Model: "judge"},
		usage.Entry{Kind: "narrative", ProjectID: pid, Actor: "apptown-system", Provider: "native", Model: "judge"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a := restartApp(t, db)
	spent, err := a.Usage.Ledger.ProjectsTotal(context.Background(), nil, pid)
	if err != nil || spent.Incomplete != 1 {
		t.Fatalf("the cut-off call is unknown spend at once: %+v %v", spent, err)
	}
	if seen, ok, err := a.Usage.Ledger.Observed(context.Background(), usage.CallKey(receipt)); err != nil || !ok || seen.Incomplete != usage.NeverSettled {
		t.Fatalf("the cut-off call is locked in: %+v %v %v", seen, ok, err)
	}
}
