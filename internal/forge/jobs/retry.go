package jobs

import (
	"context"
	"database/sql"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/jobs/internal/jobsql"

	"github.com/riverqueue/river/rivertype"
)

// RunOperations selects durable operation requests attributed to one run. This
// is queue correlation metadata; the caller owns admission and run lifecycle.
type RunOperations struct {
	Operation string
	RunID     ids.ID
}

type RetryResult struct {
	Pending bool
	Retried int
}

// RetryRunOperationsIn locks the entire selection until the caller's transaction
// ends. Terminal failures are explicitly retried through River; completed jobs
// remain completed. No workers start and no separate connection is acquired.
func RetryRunOperationsIn(ctx context.Context, tx *sql.Tx, in RunOperations) (RetryResult, error) {
	if in.Operation == "" || in.RunID.IsZero() {
		return RetryResult{}, fmt.Errorf("retry requires an operation and positive run ID")
	}
	rows, err := jobsql.New(tx).RunOperationsForUpdate(ctx, jobsql.RunOperationsForUpdateParams{Kind: (Request{}).Kind(), Operation: in.Operation, RunID: in.RunID.String()})
	if err != nil {
		return RetryResult{}, err
	}
	var out RetryResult
	var retry []int64
	for _, r := range rows {
		id, state := r.ID, rivertype.JobState(r.State)
		switch state {
		case rivertype.JobStateCancelled, rivertype.JobStateDiscarded:
			retry = append(retry, id)
			out.Pending = true
		case rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending:
			out.Pending = true
		}
	}
	if len(retry) == 0 {
		return out, nil
	}
	producer, err := Producer(nil)
	if err != nil {
		return RetryResult{}, err
	}
	for _, id := range retry {
		if _, err := producer.JobRetryTx(ctx, tx, id); err != nil {
			return RetryResult{}, err
		}
		out.Retried++
	}
	return out, nil
}
