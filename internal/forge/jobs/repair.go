package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/jobs/internal/jobsql"
	"github.com/riverqueue/river/rivertype"
)

// InterruptionRepair counts changes in the caller's transaction, not yet committed.
type InterruptionRepair struct{ Settled, Cancelled, Available int64 }

type interruptionReceipt struct {
	JobID            int64  `json:"jobId"`
	Operation        string `json:"operation"`
	Key              string `json:"key"`
	PreviousClient   string `json:"previousClient"`
	CurrentClient    string `json:"currentClient"`
	RetryOnInterrupt bool   `json:"retryOnInterrupt"`
	Reason           string `json:"reason"`
	Outcome          string `json:"outcome"`
	AttemptBefore    int    `json:"attemptBefore"`
	AttemptAfter     int    `json:"attemptAfter"`
}

// RepairInterruptedIn settles only Forge jobs left running by a previous daemon.
// The caller MUST establish exclusive startup after the old process has exited,
// before starting consumers or serving writes. A foreign client ID alone is not
// proof of death. The caller owns the transaction and must roll it back on error.
// River owns all ordinary transitions; this is a version-pinned startup exception.
// at is the caller's clock: when the interrupted jobs are settled.
func RepairInterruptedIn(ctx context.Context, tx *database.Tx, currentClientID string, at time.Time) (InterruptionRepair, error) {
	if strings.TrimSpace(currentClientID) == "" {
		return InterruptionRepair{}, fmt.Errorf("job repair requires the current client ID")
	}
	q := jobsql.New(tx.SQL())
	rows, err := q.InterruptedOperationsForUpdate(ctx, currentClientID)
	if err != nil {
		return InterruptionRepair{}, fmt.Errorf("selecting interrupted jobs: %w", err)
	}
	settledAt := at.UTC()
	var result InterruptionRepair
	for _, row := range rows {
		fail := func(err error) (InterruptionRepair, error) {
			return InterruptionRepair{}, fmt.Errorf("repairing job %d: %w", row.ID, err)
		}
		if strings.TrimSpace(row.PreviousClient) == "" {
			return fail(fmt.Errorf("missing last attempting client"))
		}
		var request Request
		if err := json.Unmarshal(row.Args, &request); err != nil {
			return fail(fmt.Errorf("invalid saved request: %w", err))
		}
		if strings.TrimSpace(request.Operation) == "" || strings.TrimSpace(request.Key) == "" {
			return fail(fmt.Errorf("saved request requires operation and key"))
		}
		receipt := interruptionReceipt{JobID: row.ID, Operation: request.Operation, Key: request.Key,
			PreviousClient: row.PreviousClient, CurrentClient: currentClientID, RetryOnInterrupt: request.RetryOnInterrupt,
			AttemptBefore: int(row.Attempt), AttemptAfter: int(row.Attempt)}
		params := jobsql.SettleInterruptedOperationParams{ID: row.ID, PreviousClient: row.PreviousClient, CurrentClient: currentClientID,
			SettledAt: settledAt, Outcome: jobsql.RiverJobStateCancelled, ErrorJson: json.RawMessage(`{}`)}
		switch {
		case row.CancelRequested:
			receipt.Reason = "cancel requested before shutdown"
		case !request.RetryOnInterrupt:
			receipt.Reason = "explicit rerun required: interrupted by shutdown"
			params.AppendError = true
			params.ErrorJson, err = json.Marshal(rivertype.AttemptError{At: settledAt, Attempt: int(row.Attempt), Error: receipt.Reason})
			if err != nil {
				return fail(err)
			}
		default:
			receipt.Reason = "interrupted by shutdown; retry permitted"
			params.Outcome = jobsql.RiverJobStateAvailable
			receipt.AttemptAfter = max(int(row.Attempt)-1, 0)
		}
		updated, err := q.SettleInterruptedOperation(ctx, params)
		if err != nil {
			return fail(err)
		}
		if len(updated) != 1 || updated[0] != row.ID {
			return fail(fmt.Errorf("settlement did not return exactly the selected job"))
		}
		receipt.Outcome = string(params.Outcome)
		if err := audit.At(settledAt).RecordIn(ctx, tx, "forge-system", ids.Nil, "jobs.interruption-settled", strconv.FormatInt(row.ID, 10), receipt); err != nil {
			return fail(err)
		}
		result.Settled++
		if params.Outcome == jobsql.RiverJobStateAvailable {
			result.Available++
		} else {
			result.Cancelled++
		}
	}
	return result, nil
}
