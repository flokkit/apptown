package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
)

// StartDefinition is the frozen definition one attempt is admitted on.
type StartDefinition struct {
	Run         workflow.PreparedRun
	Title, Body string
	Acceptance  *workflow.Acceptance
	Base        string
	Skip        string
}

// Attempts is the admission store: it owns the item's ownership, its Change
// and run, and its history.
type Attempts struct {
	db     *database.DB
	caller context.Context
	work   Work             // run admission and abandonment go through Work's doors
	clock  func() time.Time // AppTown's clock; nil is the real one
}

func (s Attempts) now() time.Time { return clock.Or(s.clock)() }

// now is AppTown's clock (nil the real one); every remediation store is
// built with it, so none falls back to the real clock unseen.
func NewAttempts(db *database.DB, work Work, caller context.Context, now func() time.Time) Attempts {
	return Attempts{db: db, work: work, caller: caller, clock: now}
}

// Project first, then pool (for admission), job and item. Completion,
// abandonment and run creation share the project lock; cancellation locks
// the job before touching its items.
func lockItem(ctx context.Context, tx *database.Tx, id ids.ID, admission bool, at time.Time) (Item, string, int, error) {
	q := queries(tx)
	named, err := q.ItemByID(ctx, id)
	if err != nil {
		return Item{}, "", 0, err
	}
	if _, err := q.LockProjectForRemediation(ctx, named.ProjectID); err != nil {
		return Item{}, "", 0, err
	}
	if admission {
		if err := q.EnsurePool(ctx, remediationsql.EnsurePoolParams{UpdatedAt: at, Kind: WorkerKindRemediation, Count: int64(DefaultWorkers)}); err != nil {
			return Item{}, "", 0, err
		}
		if _, err := q.LockPool(ctx, WorkerKindRemediation); err != nil {
			return Item{}, "", 0, err
		}
	}
	job, err := q.RemediationForUpdate(ctx, named.RemediationID)
	if err != nil {
		return Item{}, "", 0, err
	}
	held, err := q.ItemByIDForUpdate(ctx, id)
	if err != nil {
		return Item{}, "", 0, err
	}
	return item(held), job.Status, int(job.Concurrency), nil
}

func sameQueuedItem(current, snapshot Item, at time.Time) bool {
	return current.State == ItemQueued && snapshot.State == ItemQueued && current.Attempt == snapshot.Attempt &&
		current.ChangeID == snapshot.ChangeID && current.RunID == snapshot.RunID && current.Reason == snapshot.Reason && current.OutcomeRevision == snapshot.OutcomeRevision &&
		(!current.NotBefore.Valid || current.NotBefore.PassedAt(at))
}

func (s Attempts) Retry(ctx context.Context, jobID ids.ID, id ids.ID, who, note string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		item, status, _, err := lockItem(ctx, tx, id, false, now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrItemScope
		}
		if err != nil {
			return err
		}
		if item.RemediationID != jobID {
			return ErrItemScope
		}

		if item.State != ItemStuck {
			return fault.Wrap(fault.Unprocessable, fmt.Errorf("only a stuck item can be retried (this one is %s)", item.State))
		}
		if status == RemCancelled {
			return fault.New(fault.Unprocessable, "a cancelled remediation cannot be retried")
		}
		reason := "retry requested by " + who
		if strings.TrimSpace(note) != "" {
			reason += ": " + strings.TrimSpace(note)
		}
		if _, err := queries(tx).RequeueItem(ctx, remediationsql.RequeueItemParams{UpdatedAt: now, ID: id, Reason: reason}); err != nil {
			return err
		}
		if _, err := queries(tx).ResumeRemediation(ctx, remediationsql.ResumeRemediationParams{UpdatedAt: now,
			ID: item.RemediationID, Status: RemCompleted, Status_2: RemCompletedWithStuck,
		}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, item.ProjectID, "remediation.item.retry", fmt.Sprint(id), map[string]any{"note": note})
	})
}

// No work exists until ownership, supersession, change, run, first stage,
// attempt and audit have all committed. There is no half-started item to repair.
func (s Attempts) Start(ctx context.Context, snapshot Item, def StartDefinition, worker string) (*workflow.Run, error) {
	now := s.now()
	stop, err := s.work.PrepareStop(s.caller, snapshot.ProjectID)
	if err != nil {
		return nil, err
	}
	defer stop.Close()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item, status, limit, err := lockItem(s.caller, tx, snapshot.ID, true, now)
	if err != nil {
		return nil, err
	}
	if status != RemRunning || !sameQueuedItem(item, snapshot, now) {
		return nil, nil
	}
	if def.Skip != "" {
		if def.Run.Workflow.Name != "" {
			currentAdoption, err := adoption.CurrentReceiptIDIn(s.caller, tx, item.ProjectID)
			if err != nil {
				return nil, err
			}
			if currentAdoption != def.Run.AdoptionID {
				return nil, fmt.Errorf("the project's way of working changed while checking the item; check again")
			}
		}
		if _, err := queries(tx).SkipItem(s.caller, remediationsql.SkipItemParams{UpdatedAt: now, ID: item.ID, Reason: def.Skip}); err != nil {
			return nil, err
		}
		if err := audit.At(now).RecordIn(s.caller, tx, "apptown-system", item.ProjectID, "remediation.item.skip", fmt.Sprint(item.ID), map[string]any{"reason": def.Skip}); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if len(def.Run.Spec.Stages) == 0 || def.Run.Spec.Stages[0].Name == "" {
		return nil, fmt.Errorf("remediation workflow has no initial stage")
	}
	pool, err := queries(tx).PoolByKind(s.caller, WorkerKindRemediation)
	if err != nil {
		return nil, err
	}
	active, err := queries(tx).ActiveItemCounts(s.caller, item.RemediationID)
	if err != nil {
		return nil, err
	}
	capacity, busy, own := int(pool.Count), int(active.Active), int(active.InJob)
	if busy >= capacity || (limit > 0 && own >= limit) {
		return nil, nil
	}
	occupied, err := projectBusyIn(ctx, tx, item.ProjectID, item.ChangeID)
	if err != nil {
		return nil, err
	}
	if occupied {
		return nil, nil
	}
	if !item.ChangeID.IsZero() {
		eligible, err := workflow.SupersessionEligibleIn(s.caller, tx, workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID})
		if err != nil {
			return nil, err
		}
		if eligible {
			reason := fmt.Sprintf("superseded by a fresh remediation attempt (item %d)", item.ID)
			if _, err := stop.AbandonIn(s.caller, tx, workflow.StopWork{ChangeID: item.ChangeID, Reason: reason}); err != nil {
				return nil, err
			}
			if err := workflow.RecordSupersededChangeIn(s.caller, tx, workflow.SupersededChange{Change: workflow.ChangeRef{ID: item.ChangeID, ProjectID: item.ProjectID}, Reason: reason}); err != nil {
				return nil, err
			}
		}
	}
	created, err := workflow.CreateChangeIn(s.caller, tx, workflow.ChangeDraft{ProjectID: item.ProjectID, Type: "remediation", Title: def.Title, Body: def.Body, Acceptance: def.Acceptance}, "apptown-system", now)
	if err != nil {
		return nil, err
	}
	change := created.Change.ID
	run, err := s.work.AdmitRunIn(s.caller, tx, workflow.StartRequest{ProjectID: item.ProjectID, ChangeID: change, WorkflowName: def.Run.Workflow.Name, InputsJSON: "{}"}, def.Run)
	if err != nil {
		return nil, err
	}
	if _, err := queries(tx).StartItem(s.caller, remediationsql.StartItemParams{UpdatedAt: now,
		ID: item.ID, LeaseWorker: worker, LeaseUntil: sql.NullTime{Time: now.Add(remLease), Valid: true},
		ChangeID: change, RunID: run.ID,
	}); err != nil {
		return nil, err
	}
	if err := queries(tx).InsertAttempt(s.caller, remediationsql.InsertAttemptParams{ID: ids.New(now), StartedAt: now,
		ItemID: item.ID, N: int64(item.Attempt + 1), ChangeID: change, RunID: run.ID, BaseSha: def.Base,
	}); err != nil {
		return nil, err
	}
	if err := audit.At(now).RecordIn(s.caller, tx, "apptown-system", item.ProjectID, "remediation.item.started", fmt.Sprint(item.ID), map[string]any{"change": change, "run": run.ID, "attempt": item.Attempt + 1, "worker": worker}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

// Resolution fails before an attempt is admitted. Keep the previous attempt's
// identity and record the explanation without letting a stale resolver stop work.
func (s Attempts) Failed(ctx context.Context, snapshot Item, reason string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		item, status, _, err := lockItem(s.caller, tx, snapshot.ID, false, now)
		if err != nil {
			return err
		}
		if status != RemRunning || !sameQueuedItem(item, snapshot, now) {
			return nil
		}
		if _, err := queries(tx).StickItem(s.caller, remediationsql.StickItemParams{UpdatedAt: now,
			ID: item.ID, Reason: "could not open a Change: " + reason,
		}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(s.caller, tx, "apptown-system", item.ProjectID, "remediation.item.start-failed", fmt.Sprint(item.ID), map[string]any{"reason": reason, "previousAttempt": item.Attempt})
	})
}

// The queue and locked attempt admission use the same durable occupancy rule.
// The item being admitted is queued, so it is not itself an active lease.
func projectBusyIn(ctx context.Context, tx *database.Tx, project, previousChange ids.ID) (bool, error) {
	var occupied bool
	occupied, err := queries(tx).ProjectHasActiveItem(ctx, project)
	if err != nil {
		return false, err
	}
	if occupied {
		return true, nil
	}
	return workflow.ProjectBusyIn(ctx, tx, workflow.OccupancyScope{ProjectID: project, ReplacingChangeID: previousChange})
}
