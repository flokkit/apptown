package forge

import (
	"context"
	"log"
	"os"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/workflowexec"
)

const reapEvery = 5 * time.Minute

func reapLogOnly() bool { return os.Getenv("APPTOWN_REAP") != "on" }

// RunScheduler ticks every 30s: it fires the schedules due
// (changeactions.Schedules) and, every few minutes, runs the reaper
// duties. It stops between duties on daemon cancellation.
func (a *App) RunScheduler(ctx context.Context) {
	lastReap := time.Time{}
	for ctx.Err() == nil {
		now := a.cfg.clock.Now()
		changeactions.Schedules{Actions: a.changeActions, Adoption: a.Adoption, Projects: a.Projects, DB: a.DB}.FireDueSchedules(ctx, now)
		if now.Sub(lastReap) > reapEvery {
			lastReap = now
			for _, duty := range []func(){
				func() { a.reaper.Containers(ctx, reapLogOnly()) },
				// Previews nobody has looked at stop, and what a change's
				// kept goes a month after the last look.
				func() { a.Preview.Tend(ctx, reapLogOnly()) },
				func() { a.reaper.Images(ctx, reapLogOnly()) },
				func() { a.reaper.Cache(ctx, reapLogOnly()) },
				func() { a.reaper.Workspaces(ctx, reapLogOnly()) },
				func() { a.sweepExpiredCredentials(ctx) }, func() { a.sweepOldComplaints(ctx) },
				// Kept transcripts a crash left without a receipt, a day on,
				// judged by the files' modification times: the system clock.
				func() {
					if n, err := workflow.RemoveOrphanedTranscripts(ctx, a.DB, a.cfg.dataDir, clock.System().Add(-24*time.Hour)); err != nil || n > 0 {
						log.Printf("removed %d unreferenced transcripts: %v", n, err)
					}
				},
				// Scratch refs of step attempts no longer live (a crash, a
				// failed removal at settlement).
				func() {
					if n, err := workflowexec.SweepScratchRefs(ctx, a.DB, a.cfg.dataDir); err != nil || n > 0 {
						log.Printf("removed %d scratch refs of settled step attempts: %v", n, err)
					}
				},
				// Settled tasks whose release the backend never confirmed.
				func() {
					if _, err := a.tasks.ReleaseSettled(ctx); err != nil {
						log.Printf("releasing settled tasks: %v", err)
					}
				},
			} {
				if ctx.Err() != nil {
					return
				}
				duty()
			}
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
