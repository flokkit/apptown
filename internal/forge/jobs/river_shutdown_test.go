package jobs_test

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/rivertype"
)

// This version-pinned River contract fixture controls scheduling, not outcomes:
// every query still reaches the real database. Hold one fetch until shutdown's
// first refund is persisted, so it can pick up the same job before the producer
// stops. No fault or fabricated row is needed to trigger that ordering.
type shutdownFetchDriver struct {
	riverdriver.Driver[*sql.Tx]
	executor *shutdownFetchExecutor
}

func (d shutdownFetchDriver) GetExecutor() riverdriver.Executor { return d.executor }

type shutdownFetchExecutor struct {
	riverdriver.Executor
	entered           <-chan struct{}
	held, settled     chan struct{}
	blocked, refunded atomic.Bool
}

func (e *shutdownFetchExecutor) JobGetAvailable(ctx context.Context, p *riverdriver.JobGetAvailableParams) ([]*rivertype.JobRow, error) {
	select {
	case <-e.entered:
		if e.blocked.CompareAndSwap(false, true) {
			close(e.held)
			select {
			case <-e.settled:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	default:
	}
	return e.Executor.JobGetAvailable(ctx, p)
}

func (e *shutdownFetchExecutor) JobSetStateIfRunningMany(ctx context.Context, p *riverdriver.JobSetStateIfRunningManyParams) ([]*rivertype.JobRow, error) {
	rows, err := e.Executor.JobSetStateIfRunningMany(ctx, p)
	if err == nil && len(rows) > 0 && rows[0].State == rivertype.JobStateAvailable && e.refunded.CompareAndSwap(false, true) {
		close(e.settled)
	}
	return rows, err
}
