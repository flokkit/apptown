package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
)

func statusIn(ctx context.Context, tx *database.Tx, id ids.ID, to, who string, at time.Time) error {
	ends := to == RemCompleted || to == RemCompletedWithStuck || to == RemCancelled
	if _, err := queries(tx).SetRemediationStatus(ctx, remediationsql.SetRemediationStatusParams{UpdatedAt: at,
		ID: id, Status: to, Column3: ends,
	}); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "remediation."+to, fmt.Sprint(id), nil)
}

func (s Attempts) Status(ctx context.Context, id ids.ID, from []string, to, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		var current string
		held, err := queries(tx).RemediationForUpdate(s.caller, id)
		if err != nil {
			return err
		}
		current = held.Status
		if !slices.Contains(from, current) {
			return fmt.Errorf("remediation %d is not in a state that allows that", id)
		}
		if to == RemCancelled {
			if _, err := queries(tx).SkipQueuedItems(s.caller, remediationsql.SkipQueuedItemsParams{UpdatedAt: s.now(),
				RemediationID: id, Reason: "cancelled by " + who,
			}); err != nil {
				return err
			}
		}
		return statusIn(ctx, tx, id, to, who, s.now())
	})
}

// Re-read occupancy after locking the job. A completed retry cannot be hidden
// by an earlier scheduler observation that the queue was empty.
func (s Attempts) Close(ctx context.Context, id ids.ID) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		var current string
		held, err := queries(tx).RemediationForUpdate(s.caller, id)
		if err != nil {
			return err
		}
		current = held.Status
		if current != RemRunning {
			return nil
		}
		var open, stuck int
		counts, err := queries(tx).JobItemCounts(s.caller, id)
		if err != nil {
			return err
		}
		open, stuck = int(counts.Open), int(counts.Stuck)
		if open > 0 {
			return nil
		}
		to := RemCompleted
		if stuck > 0 {
			to = RemCompletedWithStuck
		}
		return statusIn(ctx, tx, id, to, "apptown-system", s.now())
	})
}

func (s Attempts) Skip(ctx context.Context, jobID ids.ID, id ids.ID, who, reason string) error {
	now := s.now()
	if strings.TrimSpace(reason) == "" {
		return fault.New(fault.Unprocessable, "skipping needs a reason — it is on the record")
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		item, _, _, err := lockItem(ctx, tx, id, false, now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrItemScope
		}
		if err != nil {
			return err
		}
		if item.RemediationID != jobID {
			return ErrItemScope
		}

		if item.State != ItemStuck && item.State != ItemQueued && item.State != ItemAccepting {
			return fault.Wrap(fault.Unprocessable, fmt.Errorf("only a queued, stuck or waiting-on-you item can be skipped (this one is %s)", item.State))
		}
		if item.State == ItemAccepting {
			if err := workflow.WithdrawApprovalsIn(ctx, tx, workflow.WithdrawApprovals{ProjectID: item.ProjectID, ChangeID: item.ChangeID, RunID: item.RunID, Withdrawal: workflow.ApprovalWithdrawal{By: who, Reason: strings.TrimSpace(reason), At: now.UTC().Format(time.RFC3339)}}, now); err != nil {
				return err
			}
			if err := recordAttemptIn(ctx, tx, item, "skipped", "skipped by "+who+": "+strings.TrimSpace(reason), now); err != nil {
				return err
			}
		}
		n, err := queries(tx).SkipItemAtRevision(ctx, remediationsql.SkipItemAtRevisionParams{UpdatedAt: now,
			ID: id, Reason: "skipped by " + who + ": " + strings.TrimSpace(reason),
			State: item.State, OutcomeRevision: item.OutcomeRevision,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fault.Wrap(fault.Conflict, fmt.Errorf("item %d changed before its skip was recorded", id))
		}

		return audit.At(now).RecordIn(ctx, tx, who, item.ProjectID, "remediation.item.skip", fmt.Sprint(id), struct {
			Reason string `json:"reason"`
		}{reason})
	})
}
