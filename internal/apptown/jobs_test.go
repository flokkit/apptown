package apptown

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

func withTestJobKind(t *testing.T, a *App, name string, max int, run jobs.Handler) {
	t.Helper()
	a.Jobs()
	kinds := apptownJobKinds(a)
	kinds[name] = jobs.Kind{Run: jobErrors(run), MaxAttempts: max}
	q, err := jobs.New(jobs.Options{}, a.DB.Pool(), kinds, jobReporter{})
	if err != nil {
		t.Fatal(err)
	}
	a.jobs = q
}

// Isolate only this fixture's selected job on a queue served by a real River
// consumer. No production claim API or handwritten worker is needed in tests.
func runQueuedJobForTest(t *testing.T, a *App, kind, key string) *rivertype.JobRow {
	t.Helper()
	var id int64
	for _, j := range operationJobs(t, a, kind) {
		if j.Key == key && (j.State == "available" || j.State == "retryable" || j.State == "scheduled") && j.ID > id {
			id = j.ID
		}
	}
	if id == 0 {
		t.Fatalf("no queued %s job for %s among %+v", kind, key, operationJobs(t, a, kind))
	}
	queue := fmt.Sprintf("test_job_%d", id)
	// sql-guarantee: Move only the selected existing job onto this test consumer queue and make it due; River JobUpdateFull has neither queue nor scheduled_at, and re-enqueueing would change the tested job identity.
	completionExec(t, a.DB, `UPDATE river_job SET queue=?,state='available',scheduled_at=now() WHERE id=?`, queue, id)
	workers := river.NewWorkers()
	river.AddWorker(workers, jobs.Worker(apptownJobKinds(a)))
	client, err := river.NewClient(riverdatabasesql.New(a.DB.Pool()), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{queue: {MaxWorkers: 1}}, PollOnly: true, TestOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer unsubscribe()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.StopAndCancel(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		return event.Job
	case <-time.After(20 * time.Second):
		t.Fatal("job did not settle")
		return nil
	}
}

// jobState is a job's queue state, read through the queue's own client.
func jobState(t *testing.T, a *App, id int64) string {
	t.Helper()
	row, err := a.Jobs().Client.JobGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return string(row.State)
}

// The sweep and the after-release hook ask for backups rather than take
// them; the loop takes them.
func TestBackupSweepEnqueues(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	_ = f
	a.SeedTargets(t.Context())
	target, err := a.Deployment.Targets.ByName(context.Background(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	main, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", docker.ByKey(main.ID).App(), 45678); err != nil {
		t.Fatal(err)
	}
	if _, err := dataJobsForTest(a).Sweep(context.Background(), "daily", nil); err != nil {
		t.Fatal(err)
	}
	(backupRequests{db: a.DB, life: a.life}).AfterRelease(idstest.N(1), "main")
	requireJobs(t, a, 1, func(row *rivertype.JobRow, r jobs.Request) bool {
		return r.Operation == "backup" && r.Key == "p/main" && row.State == rivertype.JobStateAvailable && row.MaxAttempts == 1 && !r.RetryOnInterrupt
	})
}

// Recovery asks for rebuilds; the rebuild kind refuses anything that is not
// a standing deployment on a Docker host.
func TestRebuildJobGuardsTheTarget(t *testing.T) {
	t.Parallel()
	a, _, p, _ := doWorld(t)
	cloud, _ := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	_, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", cloud.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), p.ID, "cloud", "apptown-env-"+p.Slug+"-cloud", 45679); err != nil {
		t.Fatal(err)
	}
	receipt, err := deploymentJobsForTest(a).Rebuild(context.Background(), p.Slug+"/cloud", nil)
	if err != nil || receipt["skipped"] != "not on a Docker host AppTown talks to" {
		t.Fatalf("a cloud deployment is never rebuilt locally: %v %v", receipt, err)
	}
}

// A PostgreSQL sequence advances even when the surrounding transaction rolls
// back, so precisely one real River INSERT fails without a library-specific
// production fault hook.
func failNextJobInsert(t *testing.T, db *database.DB) {
	t.Helper()
	completionExec(t, db, `CREATE SEQUENCE IF NOT EXISTS fail_job_insert_sequence`)
	// sql-guarantee: rewinds the fault sequence so exactly the next River insert fails; fault setup, not a read
	completionExec(t, db, `SELECT setval('fail_job_insert_sequence',1,false)`)
	completionExec(t, db, `CREATE OR REPLACE FUNCTION fail_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('fail_job_insert_sequence')=1 THEN RAISE EXCEPTION 'injected job insert failure'; END IF; RETURN NEW; END $$`)
	completionExec(t, db, `CREATE OR REPLACE TRIGGER fail_job_insert BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION fail_job_insert()`)
}

func deploymentJobsForTest(a *App) deployment.JobWorker {
	return deployment.JobWorker{Service: a.Deployment, Projects: a.Projects, DataDir: a.cfg.dataDir, Reporter: selfErrorsFor(a)}
}

func dataJobsForTest(a *App) dataprotection.JobWorker {
	return dataprotection.JobWorker{Service: a.DataProtection, Projects: a.Projects, Backups: backupRequests{db: a.DB, life: a.life}}
}

// Each job kind's retry budget has one source: the queue's cap for the kind
// and the budget an enqueue records come from the same table, and every
// registered kind has one (a second table once repeated each budget).
func TestEveryJobKindHasOneBudget(t *testing.T) {
	t.Parallel()
	// The registration only: no handler runs, so the composition has no
	// database and no started owners, just what building the table reads.
	a := &App{Integrations: &integration.Service{}, Usage: &usage.Service{}}
	kinds := apptownJobKinds(a)
	if len(kinds) != len(jobBudgets) {
		t.Fatalf("%d kinds, %d budgets", len(kinds), len(jobBudgets))
	}
	keys := map[string]string{workflowCompletionJob: "1/land", workflowAbandonmentJob: "change/1/cleanup"}
	for name, kind := range kinds {
		key := keys[name]
		if key == "" {
			key = "k"
		}
		budget, err := jobAttempts(name, key)
		if err != nil || budget != kind.MaxAttempts || budget < 1 {
			t.Errorf("%s: enqueue budget %d (%v), queue cap %d", name, budget, err, kind.MaxAttempts)
		}
	}
	if n, err := jobAttempts(narrativeJob, "change/1"); err != nil || n != 1 {
		t.Errorf("a narrative refresh is retried: %d %v", n, err)
	}
	if n, err := jobAttempts(workflowCompletionJob, "1/narrative"); err != nil || n != 1 {
		t.Errorf("a narrative is retried: %d %v", n, err)
	}
}

// Every job a domain transaction queues goes through the App's one
// jobs.Enqueuer, which stores it on its kind's queue with its kind's budget,
// and refuses a kind it does not know, inserting nothing. The stored rows
// are read: that is where the queue and budget live.
// Kills: an insert on the default queue whatever the kind; a fixed budget;
// an unknown kind admitted.
func TestTheJobQueueStoresEveryKindOnItsQueueWithItsBudget(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	ctx := context.Background()
	keys := map[string]string{workflowCompletionJob: "1/land", workflowAbandonmentJob: "change/1/cleanup"}
	for kind, k := range apptownJobKinds(a) {
		key := keys[kind]
		if key == "" {
			key = "k"
		}
		if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
			return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: kind, Key: key})
		}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		queued := operationJobs(t, a, kind)
		if len(queued) != 1 {
			t.Fatalf("%s: %d jobs", kind, len(queued))
		}
		row, err := a.Jobs().Client.JobGet(ctx, queued[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		want := k.Queue
		if want == "" {
			want = river.QueueDefault
		}
		if row.Queue != want || row.MaxAttempts != k.MaxAttempts {
			t.Errorf("%s stored on %q with %d attempts; registered on %q with %d", kind, row.Queue, row.MaxAttempts, want, k.MaxAttempts)
		}
	}
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: "no-such-job", Key: "k"})
	}); err == nil || len(operationJobs(t, a, "no-such-job")) != 0 {
		t.Fatalf("an unknown kind was queued: %v", err)
	}
	// The one delayed insert, the deletion pass, is due when it asked.
	after := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		return cleanupQueue{}.EnqueuePassIn(ctx, tx, idstest.N(7), after)
	}); err != nil {
		t.Fatal(err)
	}
	for _, j := range operationJobs(t, a, projectlifecycle.DeletionPassJob) {
		if j.Key != "project/"+idstest.N(7).String() {
			continue
		}
		row, err := a.Jobs().Client.JobGet(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !row.ScheduledAt.Equal(after) {
			t.Fatalf("the deletion pass is due %s, asked for %s", row.ScheduledAt, after)
		}
		return
	}
	t.Fatal("the deletion pass was not queued")
}

// A fleet review or what-if is inserted on the conformance queue, where its
// one worker is, and the queue registers the kind there: on the default
// queue four would run at once. The stored job row is read, because
// runQueuedJobForTest moves a job to its own queue.
//
// Kills: a transactional enqueue that ignores the kind's queue; a kind
// registered on another queue than its jobs are inserted on.
func TestConformanceJobsAreInsertedOnTheirQueue(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	ctx := testConformanceContext("boss")
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	rev, err := fleetReviewsFor(a).StartFleetReviewWith(ctx, "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	whatIf, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 1, Projects: []ids.ID{idstest.N(1)}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := apptownJobKinds(a)
	for kind, key := range map[string]string{verification.ReviewJob: fmt.Sprint(rev.ID), verification.WhatIfJob: fmt.Sprint(whatIf)} {
		if kinds[kind].Queue != jobs.ConformanceQueue {
			t.Errorf("%s is registered on %q", kind, kinds[kind].Queue)
		}
		found := 0
		for _, j := range operationJobs(t, a, kind) {
			if j.Key != key {
				continue
			}
			found++
			row, err := a.Jobs().Client.JobGet(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.Queue != jobs.ConformanceQueue {
				t.Errorf("%s/%s stored on %q", kind, key, row.Queue)
			}
		}
		if found != 1 {
			t.Errorf("%s/%s: %d jobs", kind, key, found)
		}
	}
}

// Remediation and eval passes each run on their own queue (one worker, set
// in jobs.New), every minute and when someone acts, within their budgets.
// Kills: a pass on the default queue, where four run at once; a pass off the
// schedule; a pass left to the worker's ten-minute default budget.
func TestPassesRunAloneOnTheirQueuesEveryMinute(t *testing.T) {
	t.Parallel()
	a := &App{Integrations: &integration.Service{}, Usage: &usage.Service{}}
	for kind, want := range map[string]struct {
		queue  string
		budget time.Duration
	}{remediation.PassJob: {jobs.RemediationQueue, 20 * time.Minute}, evals.PassJob: {jobs.EvalsQueue, 2 * time.Hour}} {
		// Its budget is set on the kind: without one, the queue worker's
		// ten-minute default applies (BQ2-01).
		if k := apptownJobKinds(a)[kind]; k.Queue != want.queue || k.MaxAttempts != 1 || k.Timeout != want.budget {
			t.Errorf("%s: queue %q, attempts %d, budget %s", kind, k.Queue, k.MaxAttempts, k.Timeout)
		}
		scheduled := 0
		for _, s := range apptownJobSchedules() {
			if s.Operation != kind {
				continue
			}
			scheduled++
			at := time.Date(2026, 10, 11, 9, 0, 30, 0, time.UTC)
			if next := s.When.Next(at); s.Key != passScheduledKey || next.Sub(at) > time.Minute {
				t.Errorf("%s: schedule key %q, next %s after %s", kind, s.Key, next, at)
			}
		}
		if scheduled != 1 {
			t.Errorf("%s: %d schedules", kind, scheduled)
		}
	}
}

// Starting or continuing an eval asks for a pass through the request the
// App binds: one "wake" job, stored on the evals queue, however often it is
// asked while it waits.
// Kills: a request on the default queue; a request with no key of its own
// (merged with the schedule's, or never merged).
func TestAnEvalPassRequestIsQueuedOnceOnItsQueue(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	ctx := context.Background()
	request := evalsDependencies(a).RequestPass
	for range 2 {
		if err := request(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var found []jobs.Job
	for _, j := range operationJobs(t, a, evals.PassJob) {
		if j.Key == passWakeKey && j.State == "available" {
			found = append(found, j)
		}
	}
	if len(found) != 1 {
		t.Fatalf("two requests queued %d passes", len(found))
	}
	row, err := a.Jobs().Client.JobGet(ctx, found[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Queue != jobs.EvalsQueue {
		t.Fatalf("the pass is stored on %q", row.Queue)
	}
}

// Attention reads must only admit one durable observation, with actual disk
// reads deferred until that job runs. A completed observation supplies evidence
// without another request-path collection or refresh job.
func TestDiskObservationUsesTheDurableQueueBeforeReading(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	ctx := t.Context()
	cold, err := a.diskObserver.Snapshot(ctx)
	if err != nil || !cold.Unknown || len(cold.Disks) != 0 {
		t.Fatalf("cold read collected disks or failed admission: %+v %v", cold, err)
	}
	for range 3 {
		if err := (diskObservationRequests{db: a.DB}).RequestDiskObservation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	queued := operationJobs(t, a, diskObservationJob)
	if len(queued) != 1 || queued[0].Key != "instance" {
		t.Fatalf("observation requests did not coalesce: %+v", queued)
	}
	if kind := apptownJobKinds(a)[diskObservationJob]; kind.Timeout != 30*time.Second || kind.MaxAttempts != 1 {
		t.Fatalf("unbounded observation job: %+v", kind)
	}
	job := runQueuedJobForTest(t, a, diskObservationJob, "instance")
	if job.State != rivertype.JobStateCompleted {
		t.Fatalf("observation job did not complete: %+v", job)
	}
	warm, err := a.diskObserver.Snapshot(ctx)
	if err != nil || len(warm.Disks) == 0 || warm.Disks[0].Reading.ReadAt.IsZero() {
		t.Fatalf("completed job did not publish disk evidence: %+v %v", warm, err)
	}
	if rows := operationJobs(t, a, diskObservationJob); len(rows) != 1 {
		t.Fatalf("warm read queued another observation: %+v", rows)
	}
}

// Target revision is part of durable observation identity: retries coalesce,
// while an edit admits a distinct observation instead of reusing old work.
func TestCapacityObservationJobsKeepTheTargetRevision(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	request := capacityObservationRequests{db: a.DB}
	id := idstest.N(88)
	for _, revision := range []int64{1, 1, 2, 2} {
		if err := request.RequestCapacityObservation(t.Context(), id, revision); err != nil {
			t.Fatal(err)
		}
	}
	rows := operationJobs(t, a, deployment.CapacityObservationJob)
	if len(rows) != 2 {
		t.Fatalf("observation identity not deduplicated: %+v", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Key] = true
	}
	if !seen[id.String()+"/1"] || !seen[id.String()+"/2"] {
		t.Fatalf("revision lost: %+v", rows)
	}
	kind := apptownJobKinds(a)[deployment.CapacityObservationJob]
	if kind.Timeout != 30*time.Second || kind.MaxAttempts != 1 {
		t.Fatalf("unbounded observation: %+v", kind)
	}
}

func TestProtectionObservationJobsKeepTheDeploymentIdentity(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	request := protectionObservationRequests{db: a.DB}
	id := idstest.N(89)
	for _, identity := range []string{strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		if err := request.RequestProtectionObservation(t.Context(), id, identity); err != nil {
			t.Fatal(err)
		}
	}
	rows := operationJobs(t, a, dataprotection.ProtectionObservationJob)
	if len(rows) != 2 {
		t.Fatalf("protection requests lost identity or deduplication: %+v", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Key] = true
	}
	if !seen[id.String()+"/"+strings.Repeat("a", 64)] || !seen[id.String()+"/"+strings.Repeat("b", 64)] {
		t.Fatalf("deployment identity changed: %+v", rows)
	}
	kind := apptownJobKinds(a)[dataprotection.ProtectionObservationJob]
	if kind.Timeout != 30*time.Second || kind.MaxAttempts != 1 {
		t.Fatalf("unbounded protection job: %+v", kind)
	}
}

func TestWorkerObservationUsesTheDurableQueueBeforeContactingWorker(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Profile: execution.WorkerProfile{Name: execution.ProfileWorkerVM}})
	a := testApp(t, Config{Worker: workerClientConfig(t, ca, srv.URL)})
	cold, err := a.workerObserver.Snapshot(t.Context())
	if err != nil || !cold.Configured || !cold.Unknown || !cold.ObservedAt.IsZero() {
		t.Fatal("cold read claimed protocol evidence", cold, err)
	}
	for range 3 {
		if err := (workerObservationRequests{db: a.DB}).RequestWorkerObservation(t.Context(), cold.Identity); err != nil {
			t.Fatal(err)
		}
	}
	queued := operationJobs(t, a, workerObservationJob)
	if len(queued) != 1 || queued[0].Key != cold.Identity {
		t.Fatal("worker requests not coalesced", queued)
	}
	kind := apptownJobKinds(a)[workerObservationJob]
	if kind.Timeout != 15*time.Second || kind.MaxAttempts != 1 {
		t.Fatal("worker job unbounded", kind)
	}
	job := runQueuedJobForTest(t, a, workerObservationJob, cold.Identity)
	if job.State != rivertype.JobStateCompleted {
		t.Fatal("worker job did not settle", job)
	}
	warm, err := a.workerObserver.Snapshot(t.Context())
	if err != nil || warm.Unknown || warm.Availability != execution.WorkerAvailable || warm.ObservedAt.IsZero() {
		t.Fatal("queued worker observation did not publish", warm, err)
	}
	if len(operationJobs(t, a, workerObservationJob)) != 1 {
		t.Fatal("warm read admitted more work")
	}
}
