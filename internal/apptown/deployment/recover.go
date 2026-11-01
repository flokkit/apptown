package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
)

// Recovery (deployment.md §14.7): an attempt AppTown was interrupted in
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

// recover observes one interrupted attempt and settles it: active, failed,
// or honestly uncertain.
func (s *Service) recover(ctx context.Context, attemptID ids.ID) (RecoveryResult, error) {
	return s.Recoverer().Run(ctx, attemptID)
}

// Recoverer is the package's recoverer over its own stores and drivers.
func (s *Service) Recoverer() recoverer {
	return recoverer{
		Store:    s.Outcomes,
		Source:   s.recoverySource(),
		Reporter: recoveryReporter{log: s.log, applied: s.applied},
	}
}

type recoverySource struct {
	allowed func(context.Context, Attempt) error
	store   *Outcomes
	driver  func(context.Context, *Deployment) (deploy.Driver, error)
}

func (s recoverySource) LoadAttempt(ctx context.Context, id ids.ID) (snapshot, error) {
	at, status, checkpoint, err := s.store.RecoveryAttempt(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		err = errAttemptAbsent
	}
	return snapshot{Attempt: at, Status: status, Checkpoint: checkpoint}, err
}

func (s recoverySource) PrepareObservation(ctx context.Context, at Attempt) (recoveryTarget, error) {
	var target recoveryTarget
	inputs, err := s.store.InputsFor(ctx, at)
	if err != nil {
		return target, fmt.Errorf("reading admitted deployment inputs: %w", err)
	}
	target.ReleaseID = inputs.ReleaseID
	dep, err := s.store.deploymentByID(ctx, at.DeploymentID)
	if err != nil {
		return target, fmt.Errorf("reading deployment: %w", err)
	}
	target.TargetID = dep.TargetID
	target.DesiredGeneration = dep.DesiredGeneration
	target.Observer = recoveryObserver{dep: dep, driver: s.driver, allowed: s.allowed, inputs: inputs}
	return target, nil
}

type recoveryObserver struct {
	allowed func(context.Context, Attempt) error
	inputs  Inputs
	dep     *Deployment
	driver  func(context.Context, *Deployment) (deploy.Driver, error)
}

func (o recoveryObserver) Resume(ctx context.Context, at Attempt, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	d, err := o.driver(ctx, o.dep)
	if err != nil {
		return deploy.Observation{}, fmt.Errorf("resolving deployment driver: %w", err)
	}
	if remote, ok := d.(*portainerRelease); ok {
		remote.ExpectedArtifact = o.inputs.ArtifactHash
		remote.Allowed = func(ctx context.Context) error {
			if o.allowed == nil {
				return errors.New("Portainer recovery has no captured current authority")
			}
			return o.allowed(ctx, at)
		}
	}
	return d.Resume(ctx, o.dep.Placement(), at.Generation, noted, logf)
}

type recoveryReporter struct {
	log     func(ids.ID, string, ...any)
	applied func(ids.ID)
}

func (r recoveryReporter) Log(releaseID ids.ID, format string, args ...any) {
	if !releaseID.IsZero() {
		r.log(releaseID, format, args...)
		return
	}
	log.Printf("deployment recovery: "+format, args...)
}
func (r recoveryReporter) Applied(targetID ids.ID) {
	if r.applied != nil {
		r.applied(targetID)
	}
}

func (s *Service) recoverySource() recoveryReader {
	return recoverySource{store: s.Outcomes, allowed: s.requireRecoveryReadiness, driver: func(ctx context.Context, dep *Deployment) (deploy.Driver, error) {
		target, err := s.Targets.ByID(ctx, dep.TargetID)
		if err != nil {
			return nil, err
		}
		return s.providers.Driver(ctx, target, dep)
	}}
}
