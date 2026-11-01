package deployment

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/clock"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// maintainedReadinessDriver owns the fixed observation protocol. Deployment
// supplies its admitted artifact and current permit, never a health predicate.
type maintainedReadinessDriver interface {
	maintainReadiness(context.Context, deploy.Placement, string, func(context.Context) error) (deploy.Observation, error)
}

// readinessEntry is evidence for one exact retained workload, independent of
// display inventory. A process restart intentionally loses its freshness.
type readinessEntry struct {
	passes, fails int
	key           [32]byte
	artifact      string
	attempted     time.Time
	observed      deploy.Observation
	err           error
}

func overlayReadiness(v RuntimeObservation, r listingRecord, e readinessEntry, now time.Time) RuntimeObservation {
	if e.key == ([32]byte{}) {
		return v
	}
	// A historical successful deployment or a running container is not a current
	// health observation. Only the separate exact-generation sample can supply it.
	compatible := v.State == RuntimeRunning && v.Rollout == "active" && v.Health != "unhealthy" && (e.observed.Running == "" || v.running == e.observed.Running)
	v.Health, v.healthObservedAt, v.healthGeneration, v.healthArtifact = "unknown", time.Time{}, 0, ""
	at := e.observed.HealthObservedAt
	if !compatible || !r.deployment.Standing() || r.deployment.HeldAfterRestore() != nil || r.deployment.DesiredGeneration != r.deployment.ObservedGeneration || v.Stale || e.err != nil || e.key != observationKey(r) || e.artifact == "" ||
		e.observed.ArtifactHash != e.artifact || e.observed.Generation != r.deployment.ObservedGeneration ||
		at.IsZero() || at.After(now) || now.Sub(at) > readinessFreshFor(e) {
		return v
	}
	v.Health, v.healthObservedAt, v.healthGeneration, v.healthArtifact = e.observed.Health, at, e.observed.Generation, e.artifact
	return v
}

// RunReadiness maintains existing recorded provider workloads. It cannot
// release or settle them, and is never invoked by a display/read operation.
func (s *Service) RunReadiness(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.refreshReadiness(ctx); err != nil && ctx.Err() == nil {
			log.Printf("deployment readiness: %v", err)
		}
		timer := time.NewTimer(s.nextReadinessWake())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) refreshReadiness(ctx context.Context) error {
	if !s.observations.readinessSweep.TryLock() {
		return nil
	}
	defer s.observations.readinessSweep.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	records, err := s.listRecords(ctx, nil)
	if err != nil {
		return err
	}
	attempted := map[ids.ID]time.Time{}
	previous := map[ids.ID]readinessEntry{}
	s.observations.mu.RLock()
	for id, e := range s.observations.readiness {
		attempted[id] = e.attempted
		previous[id] = e
	}
	s.observations.mu.RUnlock()
	sort.SliceStable(records, func(i, j int) bool {
		return attempted[records[i].deployment.ID].Before(attempted[records[j].deployment.ID])
	})
	values := map[ids.ID]readinessEntry{}
	for _, r := range records {
		if ctx.Err() != nil {
			break
		}
		if r.target == nil || !r.deployment.Standing() {
			continue
		}
		prior := previous[r.deployment.ID]
		if prior.key == observationKey(r) && !readinessDue(prior, s.now()) {
			// Cadence postpones diagnostics, never the current owner refusal.
			if err := s.requireMaintainedReadiness(ctx, r, prior.artifact); err != nil {
				values[r.deployment.ID] = readinessEntry{key: observationKey(r), attempted: s.now(), err: err}
			}
			continue
		}
		if deadline, ok := ctx.Deadline(); ok && deadline.Sub(clock.Or(nil)()) < readinessAllowance(prior) {
			continue
		}
		driver, err := s.providers.Driver(ctx, r.target, r.deployment)
		sampler, ok := driver.(maintainedReadinessDriver)
		if host, isDocker := driver.(*docker.Driver); isDocker && err == nil {
			sampler, ok = dockerReadiness{driver: host, service: s, record: r}, true
		}
		if err != nil || !ok {
			if _, managed := attempted[r.deployment.ID]; managed {
				if err == nil {
					err = errors.New("driver no longer supports maintained readiness")
				}
				values[r.deployment.ID] = readinessEntry{key: observationKey(r), attempted: s.now(), err: err}
			}
			continue
		}
		values[r.deployment.ID] = s.sampleReadiness(ctx, r, sampler)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	freshCtx, done := context.WithTimeout(parent, 5*time.Second)
	defer done()
	current, err := s.listRecords(freshCtx, nil)
	if err != nil {
		return err
	}
	keys := map[ids.ID][32]byte{}
	for _, r := range current {
		keys[r.deployment.ID] = observationKey(r)
	}
	s.observations.mu.Lock()
	defer s.observations.mu.Unlock()
	if s.observations.readiness == nil {
		s.observations.readiness = map[ids.ID]readinessEntry{}
	}
	for id, e := range s.observations.readiness {
		if key, ok := keys[id]; !ok || key != e.key {
			delete(s.observations.readiness, id)
		}
	}
	for id, e := range values {
		if key, ok := keys[id]; ok && key == e.key {
			s.observations.readiness[id] = e
		}
	}
	return nil
}

func (s *Service) sampleReadiness(ctx context.Context, r listingRecord, sampler maintainedReadinessDriver) readinessEntry {
	e := readinessEntry{key: observationKey(r), attempted: s.now()}
	in, err := s.Outcomes.InputsAtGeneration(ctx, r.deployment.ID, r.deployment.ObservedGeneration)
	if err != nil {
		e.err = err
		return e
	}
	e.artifact = in.ArtifactHash
	permit := func(ctx context.Context) error { return s.requireMaintainedReadiness(ctx, r, e.artifact) }
	if err := permit(ctx); err != nil {
		e.err = err
		return e
	}
	e.observed, e.err = sampler.maintainReadiness(ctx, r.deployment.Placement(), e.artifact, permit)
	if e.err == nil {
		e.err = permit(ctx)
	}
	s.observations.mu.RLock()
	previous := s.observations.readiness[r.deployment.ID]
	s.observations.mu.RUnlock()
	return readinessStreak(previous, e)
}

// requireMaintainedReadiness grants only the fixed diagnostic of the existing
// recorded generation. It never borrows a caller or original release's grants.
func (s *Service) requireMaintainedReadiness(ctx context.Context, original listingRecord, artifact string) error {
	dep, err := s.Deployments.ByID(ctx, original.deployment.ID)
	if err != nil {
		return err
	}
	if !dep.Standing() || dep.ObservedGeneration <= 0 || dep.DesiredGeneration != dep.ObservedGeneration || artifact == "" {
		return errors.New("Provider readiness requires a settled standing generation")
	}
	if err := dep.HeldAfterRestore(); err != nil {
		return err
	}
	if err := project.RefuseUnlessActive(ctx, s.db, dep.ProjectID); err != nil {
		return err
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return err
	}
	if observationKey(listingRecord{deployment: dep, target: target}) != observationKey(original) {
		return errors.New("Provider readiness placement changed")
	}
	open, err := s.Outcomes.HasOpenAttempt(ctx, dep.ID)
	if err != nil {
		return err
	}
	if open {
		return errors.New("Provider readiness is owned by an unfinished attempt")
	}
	in, err := s.Outcomes.InputsAtGeneration(ctx, dep.ID, dep.ObservedGeneration)
	if err != nil {
		return err
	}
	if in.ArtifactHash != artifact {
		return errors.New("Provider readiness admitted artifact changed")
	}
	return nil
}
