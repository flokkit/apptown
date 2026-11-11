package deployment

import (
	"cmp"
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

// Repair (deployment.md, the serving record): a standing deployment whose container is gone
// after a host restart is rebuilt from its serving attempt's exact inputs,
// never from the current checkout, at the serving generation and still
// serving its release. The caller restores the recorded source; the package
// decides whether a repair is due and runs it as a new attempt fenced on
// the serving attempt.

// RebuildPlan is what the package decided about a rebuild before any
// source is restored: the inputs to rebuild from and the attempt key, or
// the reason nothing is to be done.
type RebuildPlan struct {
	inputs  Inputs
	key     string
	repairs ids.ID // the serving attempt it reapplies
	skipped string // "" when the rebuild is due
	attempt ids.ID // an attempt already admitted under the key, when Skipped says so
	outcome string
	// bundle is the generation's bundle when the rebuild runs its images
	// instead of building: a digest bundle, pulled by digest, or, in an AppTown
	// with no local Docker, any bundle, whose images are on the target.
	bundle *ReleaseBundle
}

// prepareRebuild decides whether a standing deployment's serving attempt,
// as the job named it, is to be repaired: a repair already admitted under
// the key is left to observation (an open one becomes uncertain), an owned
// deployment is left alone, one serving another attempt is skipped, and
// only a Docker host's standing container is repaired.
func (s *Service) prepareRebuild(ctx context.Context, dep *Deployment, serving ids.ID) (RebuildPlan, error) {
	plan := RebuildPlan{key: fmt.Sprintf("repair-%s", serving), repairs: serving}
	if !serving.IsZero() {
		if at, status, err := s.Outcomes.AttemptByKey(ctx, dep.ID, plan.key); err == nil {
			if status.open() {
				if _, err := s.Outcomes.Uncertain(ctx, at, errors.New("rebuild execution interrupted; observing its original container")); err != nil {
					return plan, err
				}
			}
			plan.skipped, plan.attempt, plan.outcome = "rebuild already admitted; observation owns unfinished work", at.ID, status.String()
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
		plan.skipped = "deployment execution or observation owns this deployment"
		return plan, nil
	}
	if !dep.Standing() || dep.Container == "" {
		plan.skipped = "not a standing deployment with a local container"
		return plan, nil
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return plan, err
	}
	if t.Kind != KindDockerHost {
		plan.skipped = "not on a Docker host AppTown talks to"
		return plan, nil
	}
	if err := s.refusesPlacement(ctx, dep.ProjectID, t); err != nil {
		return plan, err
	}
	if serving.IsZero() {
		return plan, errRebuildUnselected
	}
	if dep.ServingAttemptID != serving {
		plan.skipped = "deployment serves another attempt since the repair was queued"
		return plan, nil
	}
	var found bool
	if plan.inputs, found, err = s.serving(ctx, dep); err != nil || !found {
		return plan, cmp.Or(err, errRebuildUnselected)
	}
	// No source build for a digest bundle (pulled by digest), nor, without
	// local Docker, for any bundle (recorded on this target).
	b, err := s.generationBundle(ctx, dep.ProjectID, plan.inputs)
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

var errRebuildUnselected = errors.New("repair has no recorded serving attempt; inspect or deploy a fresh revision")

// rebuildResult is a rebuild's outcome and, when the provider refused,
// the provider's error for the caller to report.
type rebuildResult struct {
	Skipped     string
	URL         string
	Attempt     ids.ID
	Outcome     deploymentOutcome
	ProviderErr error
}

// Rebuild runs the rebuild: the restored artifact must match the recorded
// inputs; a container already running is left alone; then a new attempt
// fenced on the serving attempt reconciles through the driver. For a
// digest bundle, and for any bundle without local Docker, it runs the
// generation's recorded images, built nothing.
func (s *Service) Rebuild(ctx context.Context, dep *Deployment, plan RebuildPlan, art deploy.Artifact) (rebuildResult, error) {
	// A project being deleted, or deleted, is not stood up again; one
	// admitted just before is in flight, which refuses its take-down.
	if err := project.RefuseUnlessActive(ctx, s.db, dep.ProjectID); err != nil {
		return rebuildResult{}, err
	}
	inputs := plan.inputs
	if art.Hash != inputs.ArtifactHash || art.Source == nil || art.Source.Revision != inputs.Revision {
		return rebuildResult{}, fmt.Errorf("%w: stored rebuild source differs from the original deployment", errRebuildUnselected)
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return rebuildResult{}, err
	}
	if err := s.checkAddressSnapshot(ctx, dep, t, inputs.Address); err != nil {
		return rebuildResult{}, err
	}
	if inputs.Address != nil {
		source := *art.Source
		ingress := inputs.Address.Ingress.Clone()
		source.Ingress = &ingress
		art.Source = &source
	}
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return rebuildResult{}, err
	}
	if dep.RestoreCheck == RestoreFilling {
		// The rebuild after AppTown was restored puts the data back before
		// anything of the app starts.
		// A fill that cannot be set up is not retried: the job settles it.
		dd, ok := d.(*docker.Driver)
		if !ok || s.filler == nil {
			return rebuildResult{}, fmt.Errorf("%w: %s waits for its data, and this AppTown cannot fill it", ErrReleaseRefused, dep.Name)
		}
		fill, err := s.filler.Fill(ctx, dep, t)
		if err != nil {
			return rebuildResult{}, fmt.Errorf("%w: %v", ErrReleaseRefused, err)
		}
		dd.Fill = fill.Run
		dd.RestoreStorage = storageRestoreRecorder{storageRecorder: storageRecorder{db: s.db, deployment: dep.ID, now: s.clock}, needs: fill.Copies}
	}
	running, err := s.providers.running(ctx, d, dep)
	if err != nil {
		return rebuildResult{}, err
	}
	if running {
		return rebuildResult{Skipped: "already running"}, nil
	}
	if plan.bundle != nil {
		parts, init, err := s.bundleImages(ctx, plan.bundle, t)
		if err != nil {
			return rebuildResult{}, err
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
		source.Services = inputs.Applied.onServices(docker.SourceServices(art))
		art.Source = &source
	}

	inputs.ReleaseID, inputs.ImageDigest, inputs.Rebuild = ids.Nil, "", true
	if err := ctx.Err(); err != nil {
		return rebuildResult{}, err
	}
	var providerErr error
	result, err := (Runner{Store: s.Outcomes, Acted: s.applied, BeforeAct: NewHostSnapshots(s.db, s.now).BeforeAct}).Run(ctx,
		Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: plan.key, Inputs: inputs, Repairs: plan.repairs},
		func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			o, err := d.Reconcile(ctx, dep.Placement(), at.Generation, art, plan.key,
				func(format string, values ...any) { log.Printf("deployment rebuild: "+format, values...) }, checkpoint)
			providerErr = err
			return o, err
		})
	out := rebuildResult{ProviderErr: providerErr}
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

// BootReconcile forgets, at a restart, the preview rows whose container its
// host says is gone or stopped: a preview's ownership is in memory and lost
// at a restart. present says what a target's host has. A standing
// deployment's lost container is repaired by its host's reader (HostReaders).
func (s *Service) BootReconcile(ctx context.Context, present func(targetID ids.ID, container string) (ContainerPresence, error)) {
	previews, err := s.Deployments.Previews(ctx)
	if err != nil {
		return
	}
	for _, p := range previews {
		// Only a definite answer forgets one: an unreachable host keeps it.
		state, perr := present(p.TargetID, p.Container)
		if p.Container == "" || (perr == nil && (state == ContainerGone || state == ContainerStopped)) {
			if err := s.Deployments.ForgetPreview(ctx, p.ChangeID); err != nil {
				log.Printf("change %d: forgetting its preview deployment: %v", p.ChangeID, err)
			}
		}
	}
}
