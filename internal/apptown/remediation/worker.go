package remediation

// The remediation pass (design/subsystems/remediation.md §3.1, §4, §5, §9),
// run as the job queue's remediation-pass job, one at a time: each pass (1) reconciles in-flight items from the facts in the tables —
// the Change's state and its runs — moving each along or stopping it with a
// reason, then (2) fills free workers with queued items, fairly across running
// jobs, one Change per project at a time, then (3) closes jobs with nothing
// left to do.
//
// Crash-safe by construction (§12.8): every transition is a compare-and-swap
// on the item's current state, and every pass (the first after a restart
// too) re-derives state from the tables before starting new work.
//
// Rate limits: a provider refusing (429 / overloaded / no credit) is a
// TRANSIENT, not the project's fault — the item goes back to the queue with a
// backoff and the whole pool holds off starting new work for a cooldown, so a
// bigger pool never turns into a faster way to hit the limit.

import (
	"context"
	"log"
	"time"
)

// PassJob is the job queue's kind for one pass. Passes run on their own
// queue, one at a time: every minute on the schedule, and at once when a
// person's action asks (Wake).
const PassJob = "remediation-pass"

const (
	remLease          = 15 * time.Minute
	Cooldown          = 3 * time.Minute // the pool holds off this long after a provider refuses
	remMaxTransients  = 3
	BusyConsoleWindow = 15 * time.Minute // a chat touched this recently means a person is in the workshop
)

// Pass stops between operations when ctx ends. External work already in
// progress retains its own timeout; a stopped pass does not claim to kill
// that work, and an item it was moving keeps its recorded state for the
// next pass.
func (s *Service) Pass(ctx context.Context) {
	s.reconcileItems(ctx)
	if ctx.Err() != nil {
		return
	}
	s.startQueuedItems(ctx)
	if ctx.Err() != nil {
		return
	}
	s.closeFinished(ctx)
}

// RunPassJob is the job queue's handler for PassJob: one pass. Its
// failures are logged by the pass; a pass stopped by shutdown or its budget
// returns why, and the next pass carries on from the tables.
func (s *Service) RunPassJob(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	s.Pass(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"pass": "done"}, nil
}

// --- (1) reconcile in-flight items ------------------------------------------------------

func (s *Service) reconcileItems(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	items, err := s.records.candidates()
	if err != nil {
		log.Printf("remediation: reading reconciliation candidates: %v", err)
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		s.ReconcileItem(ctx, item)
	}
}

// ReconcileItem moves one item along from the facts in the tables.
func (s *Service) ReconcileItem(ctx context.Context, item Item) {
	if err := s.reconciler.reconcile(ctx, item); err != nil {
		log.Printf("item %d: reconciling: %v", item.ID, err)
	}
}

// --- (2) start queued items ----------------------------------------------------------------

func (s *Service) startQueuedItems(ctx context.Context) {
	if err := s.scheduler.fill(ctx); err != nil {
		log.Printf("remediation: filling workers: %v", err)
	}
}

// --- (3) close finished jobs --------------------------------------------------------------

func (s *Service) closeFinished(ctx context.Context) {
	if err := s.schedule.closeFinished(ctx); err != nil {
		log.Printf("remediation: closing jobs: %v", err)
	}
}
