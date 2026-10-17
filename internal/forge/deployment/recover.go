package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
)

// Recovery (deployment.md §14.7): an attempt the forge was interrupted in
// is observed, never re-run. Startup queues the observation of every
// interrupted attempt before anything is dispatched; the job resumes
// through the target's driver with what the attempt had noted.

// PrepareRecovery queues observation of every interrupted attempt and
// reports how many; it contacts no provider.
func (s *Service) PrepareRecovery(ctx context.Context) (int, error) {
	report, err := PrepareRecovery(ctx, s.Outcomes)
	if err != nil {
		return 0, err
	}
	return report.Queued, nil
}

// Recover observes one interrupted attempt and settles it: active, failed,
// or honestly uncertain.
func (s *Service) Recover(ctx context.Context, attemptID ids.ID) (RecoveryResult, error) {
	return s.Recoverer().Run(ctx, attemptID)
}

// Recoverer is the package's recoverer over its own stores and drivers.
func (s *Service) Recoverer() Recoverer {
	return Recoverer{
		Store: s.Outcomes,
		Source: RecoverySourceFor(s.Outcomes, func(ctx context.Context, dep *Deployment) (deploy.Driver, error) {
			t, err := s.Targets.ByID(ctx, dep.TargetID)
			if err != nil {
				return nil, err
			}
			return s.providers.Driver(ctx, t, dep)
		}),
		Reporter: recoveryReporter{log: s.log, applied: s.applied},
	}
}

// RecoverySourceFor reads strict admitted inputs and binds an observer to
// the recorded deployment; the driver is resolved per deployment, never
// exposed whole to recovery.
func RecoverySourceFor(store *Outcomes, driver func(context.Context, *Deployment) (deploy.Driver, error)) RecoverySource {
	return recoverySource{store: store, driver: driver}
}

type recoverySource struct {
	store  *Outcomes
	driver func(context.Context, *Deployment) (deploy.Driver, error)
}

func (s recoverySource) LoadAttempt(ctx context.Context, id ids.ID) (Snapshot, error) {
	at, status, checkpoint, err := s.store.RecoveryAttempt(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrAttemptAbsent
	}
	return Snapshot{Attempt: at, Status: status, Checkpoint: checkpoint}, err
}

func (s recoverySource) PrepareObservation(ctx context.Context, at Attempt) (RecoveryTarget, error) {
	var target RecoveryTarget
	inputs, err := s.store.InputsFor(ctx, at)
	if err != nil {
		return target, fmt.Errorf("reading admitted deployment inputs: %w", err)
	}
	target.ReleaseID = inputs.ReleaseID
	dep, err := s.store.DeploymentByID(ctx, at.DeploymentID)
	if err != nil {
		return target, fmt.Errorf("reading deployment: %w", err)
	}
	target.DesiredGeneration = dep.DesiredGeneration
	target.Observer = recoveryObserver{dep: dep, driver: s.driver}
	return target, nil
}

type recoveryObserver struct {
	dep    *Deployment
	driver func(context.Context, *Deployment) (deploy.Driver, error)
}

func (o recoveryObserver) Resume(ctx context.Context, at Attempt, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	d, err := o.driver(ctx, o.dep)
	if err != nil {
		return deploy.Observation{}, fmt.Errorf("resolving deployment driver: %w", err)
	}
	return d.Resume(ctx, o.dep.Placement(), at.Generation, noted, logf)
}

type recoveryReporter struct {
	log     func(ids.ID, string, ...any)
	applied func()
}

func (r recoveryReporter) Log(releaseID ids.ID, format string, args ...any) {
	if !releaseID.IsZero() {
		r.log(releaseID, format, args...)
		return
	}
	log.Printf("deployment recovery: "+format, args...)
}
func (r recoveryReporter) Applied() {
	if r.applied != nil {
		r.applied()
	}
}
