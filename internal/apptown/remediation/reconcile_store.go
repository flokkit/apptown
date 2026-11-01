package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Reconciliation observes an item's facts and applies one decision to them.
type Reconciliation struct {
	db     *database.DB
	caller context.Context
	work   Work             // landing rechecks go through Work's door
	clock  func() time.Time // AppTown's clock; nil is the real one
}

func (s Reconciliation) now() time.Time { return clock.Or(s.clock)() }

func NewReconciliation(db *database.DB, work Work, caller context.Context, now func() time.Time) Reconciliation {
	return Reconciliation{db: db, work: work, caller: caller, clock: now}
}

func (s Reconciliation) candidates() ([]Item, error) {
	rows, err := queries(s.db).OpenItems(s.caller)
	if err != nil {
		return nil, err
	}
	items := items(rows)
	var refs []workflow.ChangeRef
	for _, item := range items {
		if item.State == ItemStuck && !item.ChangeID.IsZero() {
			refs = append(refs, workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID})
		}
	}
	states, err := workflow.ChangeStatesIn(s.caller, s.db, refs)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		if item.State != ItemStuck || states[workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID}] == workflow.ChangeShipped {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s Reconciliation) Observe(ctx context.Context, snapshot Item) (*Observation, error) {
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item, err := ReadItem(ctx, tx, snapshot.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !sameObservation(item, snapshot) {
		return nil, nil
	}
	return readObservation(s.caller, tx, item)
}

func readObservation(ctx context.Context, tx *database.Tx, item Item) (*Observation, error) {
	o := &Observation{Item: item}
	if item.State == itemVerifying {
		return o, nil // its landing has already been recorded
	}
	change, err := workflow.ReadChangeInProject(ctx, tx, workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	o.ChangeFound = change != nil
	if change != nil {
		o.ChangeState = change.State
	}
	if item.State == ItemStuck {
		attempt, err := queries(tx).AttemptOutcome(ctx, remediationsql.AttemptOutcomeParams{ItemID: item.ID, N: int64(item.Attempt), ChangeID: item.ChangeID, RunID: item.RunID})
		if _, err := database.Lookup(err); err != nil {
			return nil, err
		}
		o.AttemptOutcome = attempt.Outcome
		return o, nil
	}
	if !o.ChangeFound || o.ChangeState == workflow.ChangeAbandoned || o.ChangeState == workflow.ChangeShipped {
		return o, nil
	}
	selection := workflow.ExecutionSelection{Change: workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID}, RunID: item.RunID}
	if o.ChangeState == workflow.ChangeLanding {
		if item.State != ItemLanding {
			return o, nil
		}
		selection.RunID, selection.LatestLanding = ids.Nil, true
	}
	execution, err := workflow.ObserveExecutionIn(ctx, tx, selection)
	if err != nil {
		return nil, err
	}
	if execution != nil {
		o.Execution = *execution
	}
	return o, nil
}

func sameFacts(a, b Observation) bool {
	return sameObservation(a.Item, b.Item) && a.ChangeFound == b.ChangeFound && a.ChangeState == b.ChangeState &&
		a.AttemptOutcome == b.AttemptOutcome && a.Execution.Same(b.Execution)
}

func (s Reconciliation) Apply(ctx context.Context, observed Observation, decision decision) (*applied, error) {
	now := s.now()
	// A stopped worker rolls the decision back; the facts stay, and the
	// next pass decides again.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item, jobStatus, _, err := lockItem(s.caller, tx, observed.Item.ID, false, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !sameObservation(item, observed.Item) {
		return nil, nil
	}
	// Workflow retries, completion and step admission share the project lock.
	// Re-read the facts after acquiring it, before renewing even the lease.
	current, err := readObservation(s.caller, tx, item)
	if err != nil {
		return nil, err
	}
	if !sameFacts(observed, *current) {
		return nil, nil
	}
	result := &applied{Item: item, RunID: observed.Execution.Run.ID, Decision: decision}
	if slices.Contains([]string{ItemWorking, ItemAccepting, ItemLanding, itemVerifying}, item.State) {
		if _, err := queries(tx).ExtendItemLease(s.caller, remediationsql.ExtendItemLeaseParams{
			ID: item.ID, LeaseUntil: sql.NullTime{Time: now.Add(remLease), Valid: true},
		}); err != nil {
			return nil, err
		}
	}
	switch decision.Action {
	case actionWait, actionVerify, actionPrepare:
	case actionPhase:
		if _, err := queries(tx).SetItemState(s.caller, remediationsql.SetItemStateParams{UpdatedAt: now,
			ID: item.ID, State: decision.State, Reason: decision.Reason,
		}); err != nil {
			return nil, err
		}
		if err := audit.At(now).RecordIn(s.caller, tx, "apptown-system", item.ProjectID, "remediation.item.phase", fmt.Sprint(item.ID), map[string]any{"from": item.State, "to": decision.State, "reason": decision.Reason}); err != nil {
			return nil, err
		}
	case actionStuck, actionBackoff, actionLanded:
		outcome := Outcome{Kind: "stuck", Reason: decision.Reason}
		if decision.Action == actionBackoff {
			outcome.Kind = "backoff"
			outcome.NotBefore = now.Add(Cooldown * time.Duration(1<<item.TransientRetries))
		} else if decision.Action == actionLanded {
			outcome.Kind = "landed"
		}
		updated, err := NewOutcomes(s.db, s.clock).recordIn(ctx, tx, item, jobStatus, outcome)
		if err != nil || updated == nil {
			return nil, err
		}
		result.Item = *updated
	case ActionRecheckLanding:
		rechecked, err := s.work.RecheckLandingIn(s.caller, tx, workflow.LandingRecheck{ChangeID: item.ChangeID, RunID: observed.Execution.Run.ID,
			Remediation: &workflow.LandingRetry{ItemID: item.ID, Attempt: item.Attempt, Retries: item.TransientRetries,
				Reason: "the landing hit a provider limit; re-running its gates: " + strs.Truncate(decision.Reason, 160)}})
		if err != nil {
			return nil, err
		}
		result.Rechecked = len(rechecked.Names) > 0
	default:
		return nil, fmt.Errorf("unknown remediation decision %d", decision.Action)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
