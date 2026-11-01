package apptown

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// RecoverInterruptedWork is a required startup repair: of the database, and
// of each project hub's no-push hook. Call it before recovery can dispatch
// runs, before workers start, and before serving writes, after the previous
// daemon has exited. Constructing another App is not
// evidence that a process restarted.
func (a *App) RecoverInterruptedWork(ctx context.Context) error {
	if a.cfg.verifyOnly {
		return fmt.Errorf("restart recovery is disabled in verify-only mode")
	}
	// Step runtime tokens are derived under a key kept in the data
	// directory, so an attempt keeps its token across a restart. It is a
	// startup step, not construction work: a failure refuses the start, and
	// until it runs no step token is issued or recognised.
	if err := a.runTok.Open(filepath.Join(a.cfg.dataDir, identity.RuntimeTokenKeyFile)); err != nil {
		return err
	}
	started := a.cfg.clock.Now()
	log.Printf("startup repair: starting interrupted-work repair")
	report, err := (startupRecoveryStore{db: a.DB, ops: a.operations, now: a.cfg.clock.Now}).recover(ctx, a.jobs.Client.ID())
	if err != nil {
		log.Printf("startup repair: failed after %s: %v", a.cfg.clock.Now().Sub(started).Round(time.Millisecond), err)
		return err
	}
	// Model calls the old process sent cannot finish: their receipts close
	// as interrupted and their spend lines are locked in as unknown. The
	// AppTown's own calls go through the gateway on every instance, with or
	// without its runtime listener, so this runs on every writable start.
	if err := a.recoverInferenceRequests(ctx); err != nil {
		return fmt.Errorf("closing interrupted model calls: %w", err)
	}
	// A project being deleted or deleted keeps its chats blocked: the
	// block lives in memory, so it is taken again before serving.
	if err := a.deletion.HoldBlocks(ctx); err != nil {
		return fmt.Errorf("blocking the chats of projects being deleted: %w", err)
	}
	// A hub without its no-push hook accepts every push, past governed
	// landing: AppTown does not serve one.
	if err := a.installNoPushHooks(); err != nil {
		return fmt.Errorf("project hubs that would accept pushes past landing; repair them before starting:\n%w", err)
	}
	log.Printf("startup repair: committed in %s; interrupted steps marked for recovery=%d, river rows settled=%d, orphaned releases failed=%d, interrupted reviews ended=%d, interrupted what-ifs settled=%d, operations settled unknown=%d, queued operations requeued=%d; preserved child workflow calls=%d, steps with recorded tasks=%d, releases owned by queued jobs or deployment attempts=%d", a.cfg.clock.Now().Sub(started).Round(time.Millisecond), report.steps, report.jobs.Settled, report.releases, report.reviews, report.whatIfs, report.operations.Settled, report.operations.Requeued, report.childCalls, report.taskSteps, report.durableReleases)
	return nil
}

type startupRecoveryStore struct {
	db  *database.DB
	ops *operations.Service
	now func() time.Time // AppTown's clock, which stamps interrupted steps', reviews' and what-ifs' ends
}

// Counts describe committed repairs and work left for the recovery owners.
// A failed transaction returns no report, even if earlier statements succeeded.
type startupRepairReport struct {
	jobs                        jobs.InterruptionRepair
	operations                  operations.RestartRepair
	steps, releases, reviews    int64
	whatIfs                     int64
	childCalls, durableReleases int64
	taskSteps                   int64
}

func (s startupRecoveryStore) recover(ctx context.Context, currentClientID string) (startupRepairReport, error) {
	var report startupRepairReport
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return startupRepairReport{}, fmt.Errorf("starting restart repair: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := clock.Or(s.now)()
	// A step whose attempt recorded a task is kept running: its task may still
	// run, and the task reconciler observes it after startup (the first-slice
	// plan, step 2). Failing it here could start a second copy on retry.
	withTasks, err := tasks.UnsettledStepsIn(ctx, tx)
	if err != nil {
		return startupRepairReport{}, fmt.Errorf("reading steps with recorded tasks: %w", err)
	}
	work, err := workflow.InterruptStepsIn(ctx, tx, withTasks, workflowexec.NotContinued(), now)
	if err != nil {
		return startupRepairReport{}, err
	}
	report.steps, report.childCalls, report.taskSteps = work.Interrupted, work.PreservedCalls, int64(len(withTasks))
	if report.jobs, err = jobs.RepairInterruptedIn(ctx, tx, currentClientID, now); err != nil {
		return startupRepairReport{}, fmt.Errorf("settling interrupted jobs: %w", err)
	}
	// An operation is never resumed: one that was running reads unknown,
	// with the steps it had confirmed; a queued one whose job went with
	// the old daemon gets a job (after the jobs' own repair above).
	if s.ops != nil {
		if report.operations, err = s.ops.RepairInterruptedIn(ctx, tx); err != nil {
			return startupRepairReport{}, fmt.Errorf("settling interrupted operations: %w", err)
		}
	}
	// Unclaimed queued releases belong to jobs; persisted deployment attempts
	// belong to deployment recovery. The rule is the deployment package's,
	// and it counts the releases it preserved in this same transaction.
	keys, err := jobs.UnfinishedKeysIn(ctx, tx.Native(), deployment.ReleaseJob)
	if err != nil {
		return startupRepairReport{}, fmt.Errorf("reading queued releases: %w", err)
	}
	var queued []ids.ID
	for _, key := range keys {
		id, err := ids.Parse(key)
		if err != nil {
			// A key that is not a release id names no release, so it
			// protects none; the job itself fails when it runs.
			log.Printf("startup repair: queued release job %q names no release; ignored", key)
			continue
		}
		queued = append(queued, id)
	}
	if report.releases, report.durableReleases, err = deployment.RepairInterruptedReleasesIn(ctx, tx, queued, now); err != nil {
		return startupRepairReport{}, err
	}
	if report.reviews, err = conformance.RepairInterruptedReviews(ctx, tx, now); err != nil {
		return startupRepairReport{}, fmt.Errorf("repairing interrupted reviews: %w", err)
	}
	if report.whatIfs, err = conformance.RepairInterruptedWhatIfs(ctx, tx, now); err != nil {
		return startupRepairReport{}, fmt.Errorf("settling interrupted what-ifs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return startupRepairReport{}, fmt.Errorf("committing restart repair: %w", err)
	}
	return report, nil
}
