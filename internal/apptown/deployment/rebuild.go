package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Rebuilding (deployment.md §14): a standing deployment whose container is
// gone after a host restart is rebuilt from the exact inputs its last
// generation was admitted with, never from the current checkout. The
// caller restores the recorded source; the package decides whether a
// rebuild is due and runs it as a new attempt fenced on the generation.

// RebuildPlan is what the package decided about a rebuild before any
// source is restored: the inputs to rebuild from and the attempt key, or
// the reason nothing is to be done.
type RebuildPlan struct {
	Inputs     Inputs
	Key        string
	Generation int64
	Skipped    string // "" when the rebuild is due
	Attempt    ids.ID // an attempt already admitted under the key, when Skipped says so
	Outcome    string
	// bundle is the generation's bundle when the rebuild runs its images
	// instead of building: a digest bundle, pulled by digest, or, in an AppTown
	// with no local Docker, any bundle, whose images are on the target.
	bundle *ReleaseBundle
}

// PrepareRebuild decides whether a standing deployment is to be rebuilt
// from its selected generation: a rebuild already admitted under the key
// is left to observation (an open one becomes uncertain), an owned
// deployment is left alone, a deployment that moved on is skipped, and
// only a Docker host's standing container is rebuilt.
func (s *Service) PrepareRebuild(ctx context.Context, dep *Deployment, selectedDeployment ids.ID, selectedGeneration int64) (RebuildPlan, error) {
	plan := RebuildPlan{Key: fmt.Sprintf("rebuild-%d-from-%d", dep.ID, selectedGeneration), Generation: selectedGeneration}
	if selectedDeployment == dep.ID && selectedGeneration != 0 {
		if at, status, err := s.Outcomes.AttemptByKey(ctx, dep.ID, plan.Key); err == nil {
			if status.Open() {
				if _, err := s.Outcomes.Uncertain(ctx, at, errors.New("rebuild execution interrupted; observing its original container")); err != nil {
					return plan, err
				}
			}
			plan.Skipped, plan.Attempt, plan.Outcome = "rebuild already admitted; observation owns unfinished work", at.ID, status.String()
			return plan, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return plan, err
		}
	}
	busy, err := s.Outcomes.HasOpenAttempt(ctx, dep.ID)
	if err != nil {
		return plan, err
	}
	if busy {
		plan.Skipped = "deployment execution or observation owns this deployment"
		return plan, nil
	}
	if !dep.Standing() || dep.Container == "" {
		plan.Skipped = "not a standing deployment with a local container"
		return plan, nil
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return plan, err
	}
	if t.Kind != KindDockerHost {
		plan.Skipped = "not on a Docker host AppTown talks to"
		return plan, nil
	}
	if err := s.refusesPlacement(ctx, dep.ProjectID, t); err != nil {
		return plan, err
	}
	if selectedDeployment != dep.ID || selectedGeneration == 0 {
		return plan, ErrRebuildUnselected
	}
	if dep.DesiredGeneration != selectedGeneration || dep.ObservedGeneration != selectedGeneration {
		plan.Skipped = "deployment generation changed since rebuild was queued"
		return plan, nil
	}
	plan.Inputs, err = s.Outcomes.InputsAtGeneration(ctx, dep.ID, selectedGeneration)
	if err != nil {
		return plan, fmt.Errorf("%w: reading original rebuild inputs: %w", ErrRebuildUnselected, err)
	}
	// No source build for a digest bundle (pulled by digest), nor, without
	// local Docker, for any bundle (recorded on this target).
	b, err := s.generationBundle(ctx, dep.ProjectID, plan.Inputs)
	if err != nil {
		return plan, err
	}
	if s.hosts.settings.NoLocalDocker || (b != nil && b.ByDigest()) {
		if err := s.providers.RefusesBundle(ctx, b, t); err != nil {
			return plan, err
		}
		plan.bundle = b
	}
	return plan, nil
}

var ErrRebuildUnselected = errors.New("rebuild has no selected deployment generation; inspect or deploy a fresh revision")

// RebuildResult is a rebuild's outcome and, when the provider refused,
// the provider's error for the caller to report.
type RebuildResult struct {
	Skipped     string
	URL         string
	Attempt     ids.ID
	Outcome     Outcome
	ProviderErr error
}

// Rebuild runs the rebuild: the restored artifact must match the recorded
// inputs; a container already running is left alone; then a new attempt
// fenced on the selected generation reconciles through the driver. For a
// digest bundle, and for any bundle without local Docker, it runs the
// generation's recorded images, built nothing.
func (s *Service) Rebuild(ctx context.Context, dep *Deployment, plan RebuildPlan, art deploy.Artifact) (RebuildResult, error) {
	// A project being deleted, or deleted, is not stood up again; one
	// admitted just before is in flight, which refuses its take-down.
	if err := project.RefuseUnlessActive(ctx, s.db, dep.ProjectID); err != nil {
		return RebuildResult{}, err
	}
	inputs := plan.Inputs
	if art.Hash != inputs.ArtifactHash || art.Source == nil || art.Source.Revision != inputs.Revision {
		return RebuildResult{}, fmt.Errorf("%w: stored rebuild source differs from the original deployment", ErrRebuildUnselected)
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return RebuildResult{}, err
	}
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return RebuildResult{}, err
	}
	if dep.RestoreCheck == RestoreFilling {
		// The rebuild after AppTown was restored puts the data back before
		// anything of the app starts.
		// A fill that cannot be set up is not retried: the job settles it.
		dd, ok := d.(*docker.Driver)
		if !ok || s.filler == nil {
			return RebuildResult{}, fmt.Errorf("%w: %s waits for its data, and this AppTown cannot fill it", ErrReleaseRefused, dep.Name)
		}
		fill, err := s.filler.Fill(ctx, dep, t)
		if err != nil {
			return RebuildResult{}, fmt.Errorf("%w: %v", ErrReleaseRefused, err)
		}
		dd.Fill = fill.Run
		dd.RestoreStorage = storageRestoreRecorder{storageRecorder: storageRecorder{db: s.db, deployment: dep.ID, now: s.clock}, needs: fill.Copies}
	}
	running, err := s.providers.Running(ctx, d, dep)
	if err != nil {
		return RebuildResult{}, err
	}
	if running {
		return RebuildResult{Skipped: "already running"}, nil
	}
	if plan.bundle != nil {
		parts, init, err := s.bundleImages(ctx, plan.bundle, t)
		if err != nil {
			return RebuildResult{}, err
		}
		source := *art.Source
		source.Images, source.Init = parts, init
		art.Source = &source
		inputs.BundleID = plan.bundle.ID // the next recovery runs it too
	}
	// The generation's own profile and overrides, as it applied them: a
	// profile chosen since, or an override set since, waits for a deploy.
	if art.Source != nil {
		source := *art.Source
		source.Contract = inputs.Applied.On(source.Contract)
		art.Source = &source
	}
	if len(inputs.Applied.Services) > 0 {
		source := *art.Source
		source.Services = inputs.Applied.OnServices(docker.SourceServices(art))
		art.Source = &source
	}

	inputs.ReleaseID, inputs.ImageDigest, inputs.Rebuild = ids.Nil, "", true
	if err := ctx.Err(); err != nil {
		return RebuildResult{}, err
	}
	var providerErr error
	base := plan.Generation
	result, err := (Runner{Store: s.Outcomes, Applied: s.applied}).Run(ctx,
		Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: plan.Key, Inputs: inputs, BaseGeneration: &base},
		func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			o, err := d.Reconcile(ctx, dep.Placement(), at.Generation, art, plan.Key,
				func(format string, values ...any) { log.Printf("deployment rebuild: "+format, values...) }, checkpoint)
			providerErr = err
			return o, err
		})
	out := RebuildResult{ProviderErr: providerErr}
	if err != nil {
		return out, err
	}
	if !result.Created {
		out.Skipped = "rebuild already admitted"
		return out, nil
	}
	out.URL, out.Attempt, out.Outcome = result.Observed.URL, result.Attempt.ID, result.Outcome
	return out, nil
}

// ContainerPresence is what a boot reconcile learns about a recorded container.
type ContainerPresence uint8

const (
	ContainerGone    ContainerPresence = iota // no such container on the host
	ContainerStopped                          // exists and is not running: stopped on purpose, by the health controller or a person
	ContainerRunning
	// ContainerUnavailable: on the built-in host of an AppTown with no local
	// Docker, which this AppTown does not have (ObserveContainer).
	ContainerUnavailable
	// ContainerUnknown: it could not be observed (ObserveContainer). It says
	// nothing about whether the container runs.
	ContainerUnknown
)

// String is the presence's word in read views: "missing" for gone.
func (p ContainerPresence) String() string {
	switch p {
	case ContainerGone:
		return "missing"
	case ContainerStopped:
		return "stopped"
	case ContainerRunning:
		return "running"
	case ContainerUnavailable:
		return "unavailable"
	}
	return "unknown"
}

// BootReconcile is what a restart does for what the apptown runs itself:
// forget preview rows whose container is not running, and ask for a rebuild
// of every standing deployment whose container is GONE on a host AppTown
// talks to. A standing container that exists but is stopped stays as found —
// the health controller holds a crash loop by stopping it, a person may stop
// one too, and unless-stopped makes Docker the same promise. present says
// what a target's host has; rebuild asks for a container, given the placement.
func (s *Service) BootReconcile(ctx context.Context, present func(targetID ids.ID, container string) (ContainerPresence, error), rebuild func(p ProjectDeployment) error) {
	if previews, err := s.Deployments.Previews(ctx); err == nil {
		for _, p := range previews {
			state, perr := present(p.TargetID, p.Container)
			if perr != nil && ctx.Err() != nil {
				return // cut short: what the hosts hold is unknown, and nothing is forgotten
			}
			if p.Container == "" || state != ContainerRunning {
				if err := s.Deployments.ForgetPreview(ctx, p.ChangeID); err != nil {
					log.Printf("change %d: forgetting its preview deployment: %v", p.ChangeID, err)
				}
			}
		}
	}
	envs, err := s.Deployments.StandingWithoutOpenAttempt(ctx)
	if err != nil {
		return
	}
	for _, e := range envs {
		if e.Container == "" {
			continue // served elsewhere: nothing local to rebuild
		}
		state, err := present(e.TargetID, e.Container)
		if err != nil {
			log.Printf("deployment %s/%s: cannot inspect selected target: %v", e.Project, e.Name, err)
			continue
		}
		if state == ContainerStopped {
			log.Printf("deployment %s/%s: container %s is stopped; leaving it as found", e.Project, e.Name, e.Container)
		}
		if state != ContainerGone {
			continue
		}
		if err := rebuild(e); err != nil {
			log.Printf("deployment %s/%s: asking for a rebuild: %v", e.Project, e.Name, err)
		}
	}
}
