package remediation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/ids"
)

// Schedule reads what may be offered and closes what is finished.
type Schedule struct {
	db    *database.DB
	clock func() time.Time // the forge's clock; nil is the real one
}

func NewSchedule(db *database.DB, now func() time.Time) Schedule { return Schedule{db: db, clock: now} }

func (s Schedule) now() time.Time { return clock.Or(s.clock)() }

// Read all scheduling facts before offering any work. Read failures must not
// turn into spare capacity or a partially populated queue.
func (s Schedule) Queue(ctx context.Context) (Queue, error) {
	at := s.now()
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return Queue{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var queue Queue
	free, err := queries(tx).FreeWorkerSlots(ctx, remediationsql.FreeWorkerSlotsParams{
		Kind: WorkerKindRemediation, Count: int64(DefaultWorkers),
	})
	if err != nil {
		return Queue{}, err
	}
	queue.Free = int(free)
	if queue.Free <= 0 {
		return queue, nil
	}
	jobs, err := readRemediationQueueJobs(ctx, tx, queue.Free)
	if err != nil {
		return Queue{}, err
	}
	for i := range jobs {
		if jobs[i].Free <= 0 {
			continue
		}
		items, err := readRemediationQueueItems(ctx, tx, jobs[i].ID, at)
		if err != nil {
			return Queue{}, err
		}
		for _, item := range items {
			busy, err := projectBusyIn(ctx, tx, item.ProjectID, item.ChangeID)
			if err != nil {
				return Queue{}, err
			}
			if !busy {
				jobs[i].Items = append(jobs[i].Items, item)
			}
		}
	}
	queue.Jobs = jobs
	return queue, nil
}

func readRemediationQueueJobs(ctx context.Context, tx *database.Tx, free int) ([]QueueJob, error) {
	rows, err := queries(tx).RunningJobCapacity(ctx, int64(free))
	if err != nil {
		return nil, err
	}
	out := make([]QueueJob, 0, len(rows))
	for _, r := range rows {
		out = append(out, QueueJob{ID: r.ID, Free: int(r.Free)})
	}
	return out, nil
}

// readRemediationQueueItems is a job's queued items whose wait has passed
// by at, the forge's clock.
func readRemediationQueueItems(ctx context.Context, tx *database.Tx, job ids.ID, at time.Time) ([]Item, error) {
	rows, err := queries(tx).QueuedItemsForJob(ctx, remediationsql.QueuedItemsForJobParams{RemediationID: job, At: at})
	if err != nil {
		return nil, err
	}
	return items(rows), nil
}

func (s Schedule) CloseFinished(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	running, err := s.RunningJobs(ctx)
	if err != nil {
		return fmt.Errorf("reading remediation completion candidates: %w", err)
	}
	var failures []error
	for _, id := range running {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := NewAttempts(s.db, nil, ctx, s.clock).Close(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("remediation %d: recording completion: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

func (s Schedule) RunningJobs(ctx context.Context) ([]ids.ID, error) {
	return queries(s.db).RunningRemediationIDs(ctx)
}
