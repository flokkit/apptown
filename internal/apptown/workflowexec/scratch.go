package workflowexec

import (
	"context"
	"database/sql"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/project"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// A step's scratch ref (scm.ScratchRef) lives while its attempt does: the
// attempt's runtime token stops working when the attempt settles, and the
// ref is removed then (dropSettledScratch, deferred by the runners of
// steps on a worker) or by the next sweep (SweepScratchRefs), whichever
// comes first.

// scratchLive reports whether a step attempt may still push its scratch
// ref: its run is running and this is the step's live attempt, as the
// runtime token's own validation reads it. An unknown step is not live; a
// failed read is an error, and the ref is kept.
func scratchLive(ctx context.Context, db *database.DB) func(stepID ids.ID, attempt int64) (bool, error) {
	return func(stepID ids.ID, attempt int64) (bool, error) {
		_, observed, err := workflow.ObserveStepAttemptIn(ctx, db, stepID, attempt)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return observed.State.RequireRunning() == nil && observed.LiveAttempt, nil
	}
}

// sweepScratchIn removes hub's scratch refs whose attempts are not live.
func sweepScratchIn(ctx context.Context, db *database.DB, hub string) (int, error) {
	if !gitexec.IsRepo(hub) {
		return 0, nil
	}
	return scm.SweepScratch(ctx, hub, scratchLive(ctx, db))
}

// dropSettledScratch removes the scratch refs of source's repository whose
// attempts have settled: deferred by a step's runner, it removes that
// step's once its outcome is recorded. A failure is logged; the sweep
// tries again.
func (a *Executor) dropSettledScratch(ctx context.Context, source workflow.Source) {
	hub, err := source.Repository(a.cfg.DataDir)
	if err != nil {
		log.Printf("run %d: resolving scratch repository: %v", source.RunID, err)
		return
	}
	if _, err := sweepScratchIn(context.WithoutCancel(ctx), a.db, hub); err != nil {
		log.Printf("removing settled scratch refs: %v; the sweep tries again", err)
	}
}

// SweepScratchRefs removes, in every project's repository under dataDir,
// the scratch refs whose step attempt is no longer live: those a crash or
// a failed removal left. It returns how many it removed, and the first
// error met; other repositories are still swept.
func SweepScratchRefs(ctx context.Context, db *database.DB, dataDir string) (int, error) {
	repositories, err := project.RepositoriesOnDisk(dataDir)
	if err != nil {
		return 0, err
	}
	var removed int
	var firstErr error
	for _, repo := range repositories {
		n, err := sweepScratchIn(ctx, db, repo)
		removed += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return removed, firstErr
}
