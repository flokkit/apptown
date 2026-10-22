package remediation

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Queue is one snapshot of what may be offered: free workers and, per
// running job, its own free capacity and its eligible items.
type Queue struct {
	Free int
	Jobs []QueueJob
}

type QueueJob struct {
	ID    ids.ID
	Free  int
	Items []Item
}

// Scheduling chooses which items to offer for admission. The attempt store
// remains the authority on capacity and ownership when each attempt commits.
// A queue snapshot is consumed once, so rejected candidates cannot spin.
type Scheduler struct {
	queue   func(context.Context) (Queue, error)
	cooling func() bool
	busy    func(context.Context, ids.ID) bool
	start   func(context.Context, Item) (*workflow.Run, error)
}

func NewScheduler(queue func(context.Context) (Queue, error), cooling func() bool, busy func(context.Context, ids.ID) bool, start func(context.Context, Item) (*workflow.Run, error)) Scheduler {
	return Scheduler{queue: queue, cooling: cooling, busy: busy, start: start}
}

func (s Scheduler) Fill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cooling() {
		return nil
	}
	queue, err := s.queue(ctx)
	if err != nil {
		return fmt.Errorf("reading remediation queue: %w", err)
	}
	startedProjects := map[ids.ID]bool{}
	var failures []error
	for queue.Free > 0 {
		considered := false
		for i := range queue.Jobs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			job := &queue.Jobs[i]
			if queue.Free <= 0 || s.cooling() {
				return errors.Join(failures...)
			}
			if job.Free <= 0 || len(job.Items) == 0 {
				continue
			}
			item := job.Items[0]
			job.Items = job.Items[1:]
			considered = true
			if startedProjects[item.ProjectID] || s.busy(ctx, item.ProjectID) {
				continue
			}
			run, err := s.start(ctx, item)
			if err != nil {
				failures = append(failures, fmt.Errorf("item %d: starting remediation: %w", item.ID, err))
			}
			// A committed attempt consumes capacity even if dispatch failed.
			if run != nil {
				queue.Free--
				job.Free--
				startedProjects[item.ProjectID] = true
			}
		}
		if !considered {
			break
		}
	}
	return errors.Join(failures...)
}
