package remediation

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/audit"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/forge/workflow"
)

// Outcomes is the settlement store: an item's outcome, its history entry,
// the spend and the receipt, in one transaction.
type Outcomes struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (s Outcomes) now() time.Time { return clock.Or(s.clock)() }

func NewOutcomes(db *database.DB, now func() time.Time) Outcomes {
	return Outcomes{db: db, clock: now}
}

// Outcome is what an attempt came to.
type Outcome struct {
	Kind      string // stuck, backoff, landed, verified
	Reason    string
	NotBefore time.Time
	ReviewID  ids.ID
	Verdict   conformance.ObservationState
}

// Usage is authoritative. Refresh each attempt's working run and its children,
// then replace the item total; a second observation never adds the spend again.
// The migration uses the same statements to repair existing inflated totals.
func sameObservation(current, snapshot Item) bool {
	return current.State == snapshot.State && current.Attempt == snapshot.Attempt &&
		current.ChangeID == snapshot.ChangeID && current.RunID == snapshot.RunID &&
		current.TransientRetries == snapshot.TransientRetries && current.OutcomeRevision == snapshot.OutcomeRevision
}

func (s Outcomes) VerificationCurrent(ctx context.Context, snapshot Item) (bool, error) {
	return (VerificationScope{db: s.db, item: snapshot}).Current(ctx)
}

// Outcome, history, accounting, audit and any job reopening share the same
// transaction. The caller performs external review only after this commits.
func (s Outcomes) Record(ctx context.Context, snapshot Item, result Outcome) (*Item, error) {
	// An outcome records work that happened: it is written though the
	// caller (a stopping worker) has gone.
	ctx = context.WithoutCancel(ctx)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item, jobStatus, _, err := lockItem(ctx, tx, snapshot.ID, false, s.now())
	if err != nil {
		return nil, err
	}
	if !sameObservation(item, snapshot) {
		return nil, nil
	}
	current, err := s.RecordIn(ctx, tx, item, jobStatus, result)
	if err != nil || current == nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return current, nil
}

// The caller holds the project, job and item locks and validates its observation.
func (s Outcomes) RecordIn(ctx context.Context, tx *database.Tx, item Item, jobStatus string, result Outcome) (*Item, error) {
	now := s.now()
	active := slices.Contains([]string{ItemWorking, ItemAccepting, ItemLanding, ItemVerifying}, item.State)
	state, reason, attemptOutcome := ItemStuck, result.Reason, "stuck"
	kind := "remediation.item.stuck"
	payload := map[string]any{"reason": reason, "change": item.ChangeID, "attempt": item.Attempt, "revision": item.OutcomeRevision + 1}
	var notBefore sql.NullTime
	retries, review := item.TransientRetries, item.VerifiedReviewID
	worker, lease := "", sql.NullTime{}
	switch result.Kind {
	case "stuck":
		if !active {
			return nil, nil
		}
	case "backoff":
		if !active || item.State == ItemVerifying || retries >= remMaxTransients || result.NotBefore.IsZero() {
			return nil, fmt.Errorf("item %d cannot back off from %s", item.ID, item.State)
		}
		state, attemptOutcome, kind = ItemQueued, "transient", "remediation.item.backoff"
		reason = "backing off after the provider refused: " + result.Reason
		notBefore, retries = sql.NullTime{Time: result.NotBefore, Valid: true}, retries+1
		payload["until"], payload["why"] = result.NotBefore, result.Reason
	case "landed":
		if (!active && item.State != ItemStuck) || item.State == ItemVerifying {
			return nil, nil
		}
		ref := workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID}
		if ref.ID.IsZero() {
			return nil, nil
		}
		states, err := workflow.ChangeStatesIn(ctx, tx, []workflow.ChangeRef{ref})
		if err != nil {
			return nil, err
		}
		if states[ref] != workflow.ChangeShipped {
			return nil, nil
		}
		state, reason, attemptOutcome, kind = ItemVerifying, "", "landed", "remediation.item.landed"
		worker, lease = item.LeaseWorker, sql.NullTime{Time: now.Add(remLease), Valid: true}
		if jobStatus == RemCompleted || jobStatus == RemCompletedWithStuck {
			if err := statusIn(ctx, tx, item.RemediationID, RemRunning, "apptown-system", now); err != nil {
				return nil, err
			}
		}
	case "verified":
		if item.State != ItemVerifying {
			return nil, nil
		}
		// The change landed even if its check still fails. Keep that execution
		// fact in history; the reason and review explain the verification result.
		attemptOutcome, review = "landed", result.ReviewID
		if result.Verdict == conformance.Conforms || result.Verdict == conformance.Excepted {
			state, reason, kind = ItemDone, "", "remediation.item.done"
		}
		payload["review"], payload["verdict"] = review, result.Verdict
	default:
		return nil, fmt.Errorf("unknown remediation outcome %q", result.Kind)
	}
	payload["reason"], payload["from"], payload["to"] = reason, item.State, state
	if err := recordAttemptIn(ctx, tx, item, attemptOutcome, reason, now); err != nil {
		return nil, err
	}
	if _, err := queries(tx).SetItemOutcome(ctx, remediationsql.SetItemOutcomeParams{UpdatedAt: now,
		ID: item.ID, State: state, Reason: reason, NotBefore: notBefore, TransientRetries: int64(retries),
		VerifiedReviewID: review, LeaseWorker: worker, LeaseUntil: lease,
	}); err != nil {
		return nil, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, "apptown-system", item.ProjectID, kind, fmt.Sprint(item.ID), payload); err != nil {
		return nil, err
	}
	current, err := ReadItem(ctx, tx, item.ID)
	if err != nil {
		return nil, err
	}
	return &current, nil
}

// The caller already holds the project, job and item locks. Human rejection
// uses this same history/accounting operation inside its control transaction.
func recordAttemptIn(ctx context.Context, tx *database.Tx, item Item, attemptOutcome, reason string, at time.Time) error {
	if item.Attempt > 0 {
		n, err := queries(tx).FinishAttempt(ctx, remediationsql.FinishAttemptParams{At: at,
			ItemID: item.ID, N: int64(item.Attempt), ChangeID: item.ChangeID, RunID: item.RunID,
			Reason: reason, Outcome: attemptOutcome, Outcome2: attemptOutcome,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("item %d attempt %d has no unique matching history record", item.ID, item.Attempt)
		}
	}
	if err := queries(tx).RollUpAttemptCost(ctx, item.ID); err != nil {
		return err
	}
	if err := queries(tx).RollUpItemCost(ctx, item.ID); err != nil {
		return err
	}
	return nil
}
