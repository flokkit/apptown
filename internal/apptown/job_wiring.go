package apptown

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/apptown/schema"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

// Jobs is the application's single queue. The registry is bound once here;
// the jobs package receives operations, never App or deployment error types.
func (a *App) Jobs() *jobs.Queue { return a.jobs }

func jobErrors(run jobs.Handler) jobs.Handler {
	return func(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
		return jobReceipt(run(ctx, key, args))
	}
}
func jobReceipt(receipt map[string]any, err error) (map[string]any, error) {
	if deploy.IsTerminal(err) {
		err = jobs.Permanent(err)
	}
	return receipt, err
}

// restoreCheckJob checks deployments held after a restore against their
// providers (deployment.Service.CheckAfterRestore).
const restoreCheckJob = "restore-check"

const diskObservationJob = "disk-observation"
const workerObservationJob = "worker-observation"

func apptownJobKinds(a *App) map[string]jobs.Kind {
	followups := workflowFollowUps(a)
	deployments := deployment.JobWorker{Service: a.Deployment, Projects: a.Projects, DataDir: a.cfg.dataDir, Reporter: selfErrorsFor(a)}
	data := dataprotection.JobWorker{Service: a.DataProtection, Projects: a.Projects, Backups: backupRequests{db: a.DB, life: a.life}}
	inventory := inventoryRefresh{targets: a.targets, providers: a.targetProviders, cache: a.inventory}
	backup := databaseBackup{db: a.DB, dump: databaseDump{DB: a.DB, Docker: a.docker, Tools: a.cfg.stock.Tools, DataDir: a.cfg.dataDir}, dataDir: a.cfg.dataDir, enabled: a.cfg.usingBuiltinDB, now: a.cfg.clock.Now}
	fleet := fleetReviewsFor(a)
	disks := a.diskObserver
	workers := a.workerObserver

	kinds := map[string]jobs.Kind{
		deployment.ReleaseJob:                   {Run: jobErrors(deployments.Release), Timeout: 20 * time.Minute},
		workflowCompletionJob:                   {Run: jobErrors(followups.Complete)},
		deployment.RecoveryJob:                  {Run: jobErrors(deployments.Recover), Timeout: 20 * time.Minute},
		workflowAbandonmentJob:                  {Run: jobErrors(followups.Abandon)},
		"backup":                                {Run: jobErrors(data.Backup)},
		"inventory":                             {Run: jobErrors(inventory.Run)},
		deployment.CapacityObservationJob:       {Run: a.capacityObserver.RunJob, Timeout: 30 * time.Second},
		dataprotection.ProtectionObservationJob: {Run: a.protectionObserver.RunJob, Timeout: 30 * time.Second},
		workerObservationJob: {Run: func(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
			return nil, workers.Run(ctx, key)
		}, Timeout: 15 * time.Second},
		diskObservationJob: {Run: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			return nil, disks.Run(ctx)
		}, Timeout: 30 * time.Second},
		"rebuild": {Run: jobErrors(deployments.Rebuild), Timeout: 20 * time.Minute},
		// After AppTown was restored, its App Platform and Portainer
		// deployments are checked against their providers each minute
		// until none is left unconfirmed.
		restoreCheckJob: {Run: jobErrors(deployments.CheckAfterRestore)},
		"backup-sweep":  {Run: jobErrors(data.Sweep)},
		// Restored copies of production whose time is up are taken down
		// within minutes of it, not at the next daily sweep.
		dataprotection.RestoredCopySweepJob: {Run: jobErrors(data.RestoredCopySweep)},
		// An operation runs on its own queue, once, for up to two hours;
		// its row in the ledger says how far it got.
		operations.Job:              {Run: a.operations.JobHandler, Timeout: jobs.OperationsTimeout},
		"db-backup":                 {Run: jobErrors(backup.Run)},
		projectlifecycle.CleanupJob: {Run: projectCleanupFor(a).Run},
		// The deletion pass takes deleting projects' deployments down one
		// at a time; the purge pass admits due purges as operations.
		projectlifecycle.DeletionPassJob: {Run: a.deletion.Pass},
		projectlifecycle.PurgePassJob:    {Run: a.deletion.PurgePass},
		// A fleet review or what-if someone asked for runs once, one at a
		// time, for up to two hours; its record says how far it got.
		verification.ReviewJob: {Run: fleet.RunReviewJob, Timeout: jobs.OperationsTimeout},
		verification.WhatIfJob: {Run: fleet.RunWhatIfJob, Timeout: jobs.OperationsTimeout},
		// One remediation pass, every minute and when a person acts.
		remediation.PassJob: {Run: a.Fixes.RunPassJob, Timeout: jobs.DefaultTimeout},
		// One eval pass, every minute and when an eval is started or
		// continued, for up to two hours: it compares landed arms by
		// running their checks.
		evals.PassJob: {Run: a.Evals.RunPassJob, Timeout: jobs.OperationsTimeout},
		// A narrative refresh asked for by a chat or an import.
		narrativeJob: {Run: a.narratives.RunJob},
	}
	for kind, k := range kinds {
		k.MaxAttempts = jobBudgets[kind]
		if queue, ok := jobQueues[kind]; ok {
			k.Queue = queue
		}
		kinds[kind] = k
	}
	return kinds
}

// jobBudgets is each job kind's attempt budget: the queue's cap for the kind
// and the budget every enqueue of it records. It is the one source for both,
// so they cannot drift.
var jobBudgets = map[string]int{
	deployment.ReleaseJob:                   1,
	workflowCompletionJob:                   3,
	deployment.RecoveryJob:                  3,
	workflowAbandonmentJob:                  3,
	"backup":                                1,
	"inventory":                             2,
	diskObservationJob:                      1,
	workerObservationJob:                    1,
	deployment.CapacityObservationJob:       1,
	dataprotection.ProtectionObservationJob: 1,
	"rebuild":                               1,
	restoreCheckJob:                         1,
	"backup-sweep":                          1,
	dataprotection.RestoredCopySweepJob:     1,
	operations.Job:                          1,
	"db-backup":                             1,
	projectlifecycle.CleanupJob:             projectlifecycle.CleanupAttempts,
	projectlifecycle.DeletionPassJob:        1,
	projectlifecycle.PurgePassJob:           1,
	verification.ReviewJob:                  1,
	verification.WhatIfJob:                  1,
	remediation.PassJob:                     1,
	evals.PassJob:                           1,
	narrativeJob:                            1,
}

// jobQueues is the queue of each kind not on the default one, for both the
// queue's registration and every enqueue of the kind, so a job is always
// inserted where its worker is.
var jobQueues = map[string]string{
	operations.Job:         jobs.OperationsQueue,
	verification.ReviewJob: jobs.ConformanceQueue,
	verification.WhatIfJob: jobs.ConformanceQueue,
	remediation.PassJob:    jobs.RemediationQueue,
	evals.PassJob:          jobs.EvalsQueue,
}

// jobAttempts is the budget one enqueue records: its kind's, except that
// model work (a narrative) runs once. Model work does not inherit the
// automatic retry policy of the cleanup operations sharing its dispatcher.
// Keys a kind does not know are refused.
func jobAttempts(kind, key string) (int, error) {
	switch kind {
	case workflowCompletionJob:
		_, effect, ok := strings.Cut(key, "/")
		if !ok {
			return 0, fmt.Errorf("invalid completion key %q", key)
		}
		switch effect {
		case "narrative":
			return 1, nil
		case "cleanup", "adopt", "parent", "land":
			return jobBudgets[kind], nil
		}
	case workflowAbandonmentJob:
		if strings.HasSuffix(key, "/narrative") {
			return 1, nil
		}
		if strings.HasPrefix(key, "change/") && strings.HasSuffix(key, "/cleanup") {
			return jobBudgets[kind], nil
		}
	default:
		if budget, ok := jobBudgets[kind]; ok {
			return budget, nil
		}
	}
	return 0, fmt.Errorf("unknown job operation %q (%s)", kind, key)
}

// jobQueue is the App's jobs.Enqueuer: every job a domain transaction
// queues goes through it.
type jobQueue struct{}

func (jobQueue) EnqueueIn(ctx context.Context, tx *database.Tx, r jobs.Request) error {
	return insertJobIn(ctx, tx, r, time.Time{})
}

// insertJobIn is AppTown's one transactional job insert: on the kind's
// queue, with the attempt budget for its key, due after (zero: now). Only
// the deletion pass asks for a later start.
func insertJobIn(ctx context.Context, tx *database.Tx, r jobs.Request, after time.Time) error {
	max, err := jobAttempts(r.Operation, r.Key)
	if err != nil {
		return err
	}
	_, err = jobs.InsertOnTx(ctx, tx.Native(), jobQueues[r.Operation], jobs.Request{Operation: r.Operation, Key: r.Key, Values: r.Values}, max, after)
	return err
}

// passRequests queues a pass a person's action asks for, of a kind that
// runs passes over its owner's tables (remediation's, evals').
type passRequests struct {
	db   *database.DB
	kind string
}

func (p passRequests) Request(ctx context.Context) error {
	return p.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: p.kind, Key: passWakeKey})
	})
}

// A pass a person's action asks for, and the scheduled one, are keyed
// apart: the queue coalesces each while it is queued or running.
const (
	passWakeKey      = "wake"
	passScheduledKey = "scheduled"
)

func apptownJobSchedules() []jobs.Schedule {
	var schedules []jobs.Schedule
	for _, spec := range []struct{ operation, cron, key string }{{"db-backup", "0 2 * * *", ""}, {"backup-sweep", "0 3 * * *", ""}, {dataprotection.RestoredCopySweepJob, "*/5 * * * *", ""},
		{projectlifecycle.DeletionPassJob, "*/5 * * * *", ""}, {projectlifecycle.PurgePassJob, "0 4 * * *", ""},
		{remediation.PassJob, "* * * * *", passScheduledKey}, {evals.PassJob, "* * * * *", passScheduledKey},
		{restoreCheckJob, "* * * * *", passScheduledKey}} {
		when, err := schema.ParseCron(spec.cron)
		if err != nil {
			panic(err)
		}
		key := spec.key
		if key == "" {
			key = "daily"
		}
		schedules = append(schedules, jobs.Schedule{Operation: spec.operation, Key: key, When: when})
	}
	return schedules
}

type jobReporter struct{}

func (jobReporter) Error(err error) { log.Printf("jobs: %v", err) }
func (jobReporter) Panic(where string, value any, stack []byte) {
	if selfPanicReporter != nil {
		selfPanicReporter(where, value, stack)
	}
}
