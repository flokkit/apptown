package scm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/strs"
)

// Service is the supported operation API. Store access and merge transitions
// stay private. Work authorizes every claim; the driver has no database handle.
type Service struct {
	records  mergeStore
	work     Work
	locks    *RepositoryLocks
	branches *Branches
}

// New builds SCM's merges; now is the forge's clock (nil the real one).
func New(db *database.DB, work Work, locks *RepositoryLocks, now func() time.Time) *Service {
	branches := NewBranches(db, locks)
	branches.Now = now
	return &Service{records: mergeStore{db, now}, work: work, locks: locks, branches: branches}
}
func (s *Service) claim(ctx context.Context, purpose Purpose, runID ids.ID) (Claim, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.work == nil || s.records.db == nil || runID.IsZero() {
		return nil, ErrCaller
	}
	claim, err := s.work.Prepare(ctx, purpose, runID)
	if err != nil {
		return nil, err
	}
	if claim == nil || claim.Source().RunID != runID {
		return nil, ErrCaller
	}
	if err := claim.Source().validate(); err != nil {
		return nil, err
	}
	return claim, nil
}
func (s *Service) Read(ctx context.Context, runID ids.ID) (Record, error) {
	claim, err := s.claim(ctx, Observe, runID)
	if err != nil {
		return Record{}, err
	}
	prepared, err := s.records.prepare(ctx, claim, Observe)
	if err != nil {
		return Record{}, err
	}
	return prepared.record(), nil
}

func (s *Service) Merge(ctx context.Context, runID ids.ID, driver Driver) (Result, error) {
	claim, err := s.claim(ctx, NewEffect, runID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Source: claim.Source()}
	if driver == nil {
		return result, fmt.Errorf("SCM merge driver is unavailable")
	}
	release, err := s.locks.Acquire(ctx, result.Source.ProjectID)
	if err != nil {
		return result, err
	}
	defer release()
	prepared, err := s.records.prepare(ctx, claim, NewEffect)
	if err != nil {
		return refusedResult(result, err)
	}
	if !result.Source.RepositoryBacked() {
		result.Merged = true
		return result, nil
	}
	if prepared.intent.State == Pending {
		if result.Source.MR.IID != 0 {
			return result, fmt.Errorf("resolve the pending local merge before using a merge request")
		}
		observed, err := s.inspectPrepared(ctx, prepared, driver)
		if err != nil {
			return result, err
		}
		if observed.Kind == Unresolved {
			return result, ErrUnresolved
		}
		prepared, err = s.records.prepare(ctx, claim, NewEffect)
		if err != nil {
			return refusedResult(result, err)
		}
	}
	// Only a landing merges, and it merges exactly its pinned candidate: it
	// publishes nothing. A landing admitted before candidates were pinned is
	// restarted, not merged from its workspace.
	if result.Source.Candidate == "" {
		if result.Source.PinRequired {
			return result, fmt.Errorf("this landing has no pinned candidate; it cannot merge")
		}
		return result, fmt.Errorf("this landing was admitted before candidates were pinned; restart it to land")
	}
	previous := ""
	if prepared.intent.State == Recorded {
		previous = prepared.intent.Result
	}
	plan, err := driver.Prepare(ctx, result.Source, previous)
	if err != nil {
		return result, err
	}
	if err := validatePlan(result.Source, plan); err != nil {
		return result, err
	}
	switch plan.Kind {
	case Empty:
		result.Merged = true
		return result, nil
	case Blocked:
		result.Conflicts, result.Drift, result.DriftDiff = plan.Conflicts, plan.Drift, plan.DriftDiff
		return result, nil
	case Existing:
		if plan.ReuseRecorded {
			if prepared.intent.State != Recorded || prepared.intent.Result != plan.Result {
				return result, ErrChanged
			}
			result.Operation = prepared.intent.Operation
		} else {
			operation, err := observationID(result.Source)
			if err != nil {
				return result, err
			}
			if err := prepared.observe(ctx, operation, plan.Result); err != nil {
				return result, err
			}
			result.Operation = operation
		}
		result.Merged, result.SHA = true, plan.Result
		return result, nil
	case LocalMerge:
		operation, err := operationID("land-")
		if err != nil {
			return result, err
		}
		prepared, err = prepared.begin(ctx, operation, plan.Expected)
		if err != nil {
			return refusedResult(result, err)
		}
		execution, effectErr := driver.Execute(ctx, result.Source, plan, operation)
		if execution.Result != "" && (execution.Unchanged || len(execution.Conflicts) != 0) {
			return result, fmt.Errorf("merge driver returned contradictory effect evidence; inspect the pending operation")
		}
		if len(execution.Conflicts) != 0 && !execution.Unchanged {
			return result, fmt.Errorf("merge conflict did not prove the target unchanged; inspect the pending operation")
		}
		if execution.Unchanged {
			if err := prepared.cancel(ctx, "merge did not change the default branch"); err != nil {
				return result, err
			}
			if effectErr != nil {
				return result, effectErr
			}
			if len(execution.Conflicts) == 0 {
				return result, fmt.Errorf("merge driver reported no effect and no conflict")
			}
			result.Conflicts = execution.Conflicts
			return result, nil
		}
		if effectErr != nil {
			return result, effectErr
		}
		if execution.Result == "" {
			return result, fmt.Errorf("merge driver did not return its committed result; inspect the pending operation")
		}
		if err := prepared.settle(ctx, execution.Result); err != nil {
			return result, fmt.Errorf("merge happened but its outcome was not recorded; retry to settle it: %w", err)
		}
		result.Merged, result.SHA, result.Operation = true, execution.Result, operation
		return result, nil
	case ProviderMerge:
		if err := prepared.revalidate(ctx); err != nil {
			return refusedResult(result, err)
		}
		operation, err := observationID(result.Source)
		if err != nil {
			return result, err
		}
		execution, err := driver.Execute(ctx, result.Source, plan, operation)
		if err != nil {
			return result, err
		}
		if execution.Result == "" || execution.Unchanged || len(execution.Conflicts) != 0 {
			return result, fmt.Errorf("provider did not return a confirmed merge result")
		}
		if err := prepared.observe(ctx, operation, execution.Result); err != nil {
			return result, fmt.Errorf("merge request merged but its outcome was not recorded; retry to settle it: %w", err)
		}
		result.Merged, result.SHA, result.Operation = true, execution.Result, operation
		return result, nil
	default:
		return result, fmt.Errorf("unknown merge plan %d", plan.Kind)
	}
}
func refusedResult(result Result, err error) (Result, error) {
	var refused *denied
	if errors.As(err, &refused) {
		result.Refusal = refused.refusal
		return result, nil
	}
	return result, err
}
func validatePlan(source Source, p Prepared) error {
	if p.ReuseRecorded && (p.Kind != Existing || source.MR.IID != 0) {
		return fmt.Errorf("only a proven existing merge can reuse its result")
	}
	switch p.Kind {
	case Empty:
		if p.Expected != "" || p.Result != "" || len(p.Conflicts)+len(p.Drift) != 0 {
			return fmt.Errorf("empty merge plan contains an effect")
		}
	case Existing:
		if p.Result == "" {
			return fmt.Errorf("existing merge requires a proven result")
		}
	case LocalMerge, ProviderMerge:
		if p.Expected == "" || p.Result != "" || len(p.Conflicts)+len(p.Drift) != 0 {
			return fmt.Errorf("new merge requires its exact source/target observation")
		}
		if (p.Kind == ProviderMerge) != (source.MR.IID != 0) {
			return fmt.Errorf("merge plan changed the recorded provider")
		}
	case Blocked:
		if len(p.Conflicts)+len(p.Drift) == 0 {
			return fmt.Errorf("blocked merge plan has no reason")
		}
	default:
		return fmt.Errorf("unknown merge plan %d", p.Kind)
	}
	return nil
}
func operationID(prefix string) (string, error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}
func observationID(source Source) (string, error) {
	if source.MR.IID != 0 {
		return fmt.Sprintf("mr:%s!%d", source.MR.Project, source.MR.IID), nil
	}
	return operationID("observe-")
}

func (s *Service) Inspect(ctx context.Context, runID ids.ID, driver Driver) (Inspection, error) {
	claim, err := s.claim(ctx, Observe, runID)
	if err != nil {
		return Inspection{}, err
	}
	if driver == nil {
		return Inspection{}, fmt.Errorf("SCM inspection driver is unavailable")
	}
	release, err := s.locks.Acquire(ctx, claim.Source().ProjectID)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	prepared, err := s.records.prepare(ctx, claim, Observe)
	if err != nil {
		return Inspection{}, err
	}
	if prepared.intent.State != Pending {
		return Inspection{}, nil
	}
	return s.inspectPrepared(ctx, prepared, driver)
}
func (s *Service) inspectPrepared(ctx context.Context, p *preparedMerge, driver Driver) (Inspection, error) {
	if !p.source.RepositoryBacked() {
		return Inspection{}, fmt.Errorf("pending merge has no repository; inspect its source before retrying")
	}
	observed, err := driver.Inspect(ctx, p.source, p.record())
	if err != nil {
		return Inspection{}, err
	}
	switch observed.Kind {
	case Found:
		if observed.Result == "" {
			return Inspection{}, fmt.Errorf("merge inspection supplied no proven result")
		}
		err = p.settle(ctx, observed.Result)
	case Unchanged:
		if observed.Result != "" {
			return Inspection{}, fmt.Errorf("unchanged merge inspection supplied a result")
		}
		err = p.cancel(ctx, "the admitted merge did not change the default branch")
	case Unresolved:
		if observed.Result != "" {
			return Inspection{}, fmt.Errorf("unresolved merge inspection supplied a result")
		}
		err = p.reportUnresolved(ctx, ErrUnresolved.Error())
	default:
		return Inspection{}, fmt.Errorf("unknown merge inspection result %d", observed.Kind)
	}
	if err != nil {
		return Inspection{}, err
	}
	return observed, nil
}
func (s *Service) InspectPending(ctx context.Context, driver Driver) (Summary, error) {
	var summary Summary
	if s == nil || s.work == nil || s.records.db == nil {
		return summary, ErrCaller
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if err := s.work.RequireInspection(ctx); err != nil {
		return summary, err
	}
	pending, err := s.records.pending(ctx)
	if err != nil {
		return summary, err
	}
	// One merge that cannot be inspected (its repository gone, its record
	// unreadable) stays pending, holding its landing, and the others are
	// still inspected: one record never stops the rest.
	var failed []error
	for _, id := range pending {
		observed, err := s.Inspect(ctx, id, driver)
		if err != nil {
			summary.Failed++
			failed = append(failed, fmt.Errorf("inspecting merge for run %d: %w", id, err))
			continue
		}
		switch observed.Kind {
		case Found:
			summary.Recorded++
		case Unchanged:
			summary.Cancelled++
		case Unresolved:
			summary.Unresolved++
		default:
			continue // Another admitted inspection already settled this record.
		}
		summary.Inspected++
	}
	if len(failed) > 0 {
		return summary, fmt.Errorf("%w: %w", ErrLeftPending, errors.Join(failed...))
	}
	return summary, nil
}

// LandingMoved: the branch or the default branch is no longer where the
// landing's gates checked it. Merging now would produce a tree nobody checked.
type LandingMoved struct {
	What, Pinned, Found string
}

func (e *LandingMoved) Error() string {
	if e.What == "main" {
		return fmt.Sprintf("the default branch moved since this landing's checks (checked against %s, now %s); restart the landing to catch up and check again", strs.ShortSHA(e.Pinned), strs.ShortSHA(e.Found))
	}
	return fmt.Sprintf("the change's branch moved since this landing's checks (checked %s, now %s); recheck the landing", strs.ShortSHA(e.Pinned), strs.ShortSHA(e.Found))
}
