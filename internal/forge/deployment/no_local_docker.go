package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

// Releases in a forge with no local Docker (design/operations/workers.md,
// "Running AppTown without a local Docker daemon"). Such a forge releases only to attached Docker
// hosts, whose own daemons build and hold the images. A bundle names the
// target it was read from, and its images are reused there and nowhere
// else. A bundle recorded before bundles named their target is refused,
// never rebuilt from source and never given a target inferred from today's
// placement. A bundle published to the project's registry (a digest bundle,
// built on the worker) goes by promotion to attached hosts, Portainer and
// App Platform too (design/operations/workers.md, "Releases built on the
// worker"). Outside the mode none of this applies.

// ErrNoLocalDocker is the built-in Docker host in a forge with no local
// Docker: unavailable, never "not running".
var ErrNoLocalDocker = fault.New(fault.Unprocessable, "this AppTown has no local Docker")

// ErrReleaseRefused is a release, or a deployment's recovery, that this
// forge refuses before preparing anything; its message names the next action.
var ErrReleaseRefused = fault.New(fault.Unprocessable, "release refused")

// errBuiltInHostRefused is a release to the built-in Docker host in a forge
// with no local Docker.
var errBuiltInHostRefused = fmt.Errorf("%w: %w; attach a Docker host and deploy there", ErrReleaseRefused, ErrNoLocalDocker)

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrReleaseRefused}, args...)...)
}

// RefusesLanding says why a landing of projectID cannot deploy to t: an
// eval copy anywhere but an eval target, or any other project on one
// (refusesPlacement); and, in a forge with no local Docker, anything but an
// attached Docker host, since a landing deploys to a Docker host and the
// built-in host is unavailable.
func (s *Service) RefusesLanding(ctx context.Context, projectID ids.ID, t *Target) error {
	if t == nil {
		return nil
	}
	if err := s.refusesPlacement(ctx, projectID, t); err != nil {
		return err
	}
	if !s.hosts.settings.NoLocalDocker {
		return nil
	}
	switch {
	case t.Kind == KindDockerHost && t.Origin == OriginAttached:
		return nil
	case t.Kind == KindDockerHost:
		return errBuiltInHostRefused
	default:
		return refused("a landing deploys to a Docker host, and %s is %s: attach a Docker host and deploy there, then promote to %s", t.Name, t.Kind, t.Kind)
	}
}

// refusesPlatform is the one platform rule for a digest bundle: a target
// that cannot say what it runs (asked returned err), or runs another
// platform than the bundle was built for, is refused before any effect.
func refusesPlatform(b *ReleaseBundle, target, platform string, asked error) error {
	return refusesBuiltPlatform(b.BuiltFrom.Platform, target, platform, asked)
}

// refusesBuiltPlatform is refusesPlatform for images built for built.
func refusesBuiltPlatform(built, target, platform string, asked error) error {
	if asked != nil {
		return refused("%s's platform is unknown: %v", target, asked)
	}
	if platform != built {
		return refused("this release was built for %s, and %s runs %s", built, target, platform)
	}
	return nil
}

// LandingTarget is the target a landing creates its missing deployment
// name on, with attrs. Outside the mode it is the built-in host. In a forge
// with no local Docker it is the one attached Docker host offered to the
// project for attrs: with none or several there is no choice the forge can
// make, and the refusal names the one a person makes.
func (s *Service) LandingTarget(ctx context.Context, projectID ids.ID, name string, attrs Attributes) (*Target, error) {
	purpose, err := s.env.Purpose(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if purpose.EvalDomainOnly() {
		return s.evalLandingTarget(ctx, name)
	}
	if !s.hosts.settings.NoLocalDocker {
		t, err := s.Targets.ByName(ctx, s.hosts.settings.BuiltInTarget)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("deployment %s does not exist and this AppTown has no built-in Docker host to create it on; attach a Docker host and create %s there", name, name)
		}
		return t, err
	}
	if s.configuration.Projects == nil {
		return nil, errors.New("deployment configuration is not configured")
	}
	project, err := s.configuration.Projects.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return nil, err
	}
	var offered []*Target
	for _, t := range targets {
		if t.Kind != KindDockerHost || t.Origin != OriginAttached || !t.Status.Offerable() || t.Reserved() != "" {
			continue
		}
		offer, why := s.Targets.OfferFor(ctx, t, project.OwnerGroup)
		if offer == nil {
			_ = why // not offered to this project: not a candidate
			continue
		}
		standing, err := s.Deployments.StandingCount(ctx, projectID, t.ID, name)
		if err != nil {
			return nil, err
		}
		if offer.Permits(t, project.Slug, attrs, standing) == "" {
			offered = append(offered, t)
		}
	}
	switch len(offered) {
	case 1:
		return offered[0], nil
	case 0:
		return nil, refused("deployment %s does not exist and no attached Docker host is offered to %s for it; offer one to the project under Integrations → Deployment targets, then land again", name, project.Slug)
	default:
		names := make([]string, len(offered))
		for i, t := range offered {
			names[i] = t.Name
		}
		return nil, refused("deployment %s does not exist and several attached Docker hosts are offered to %s (%s); create %s on the one it should run on, then land again", name, project.Slug, strings.Join(names, ", "), name)
	}
}

// bundleImages are the images a Docker host runs for b, building nothing:
// a digest bundle's, which the boot pulls by digest once the host's daemon
// says it runs the bundle's platform, or, in a forge with no local
// Docker, b's IDs verified present on t (recordedImages). nil, outside the
// mode, for a bundle of image IDs: the host builds its source, as before.
// Promotion and recovery both resolve a bundle here, before any effect.
func (s *Service) bundleImages(ctx context.Context, b *ReleaseBundle, t *Target) (parts, init map[string]string, err error) {
	switch {
	case b != nil && b.ByDigest():
		platform, asked := s.TargetPlatform(ctx, t)
		if err := refusesPlatform(b, t.Name, platform, asked); err != nil {
			return nil, nil, err
		}
		return b.Parts, b.Init, nil
	case s.hosts.settings.NoLocalDocker:
		images, err := s.recordedImages(ctx, b, t)
		if err != nil {
			return nil, nil, err
		}
		return images.Parts, images.Init, nil
	}
	return nil, nil, nil
}

// recordedImages are b's images, verified present on t's daemon. A daemon
// that cannot be asked is an error, not a refusal.
func (s *Service) recordedImages(ctx context.Context, b *ReleaseBundle, t *Target) (*docker.RecordedImages, error) {
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	images := &docker.RecordedImages{Parts: b.Parts, Init: b.Init, SelfServing: b.BuiltFrom.SelfServing}
	if err := images.Present(ctx, dk); errors.Is(err, docker.ErrRecordedImageMissing) {
		return nil, refused("this release's images live only on %s, which no longer has them (%v); land the change again", t.Name, err)
	} else if err != nil {
		return nil, err
	}
	return images, nil
}

// generationBundle is the bundle a deployment generation ran: the bundle a
// recovery named, its manual release's bundle, or the bundle its landing
// cut. nil when there is none.
func (s *Service) generationBundle(ctx context.Context, projectID ids.ID, in Inputs) (*ReleaseBundle, error) {
	q := deploymentsql.New(s.db.SQL())
	switch {
	case !in.BundleID.IsZero():
		b, err := lookupBundle(s.Bundles.ByID(ctx, in.BundleID))
		if b != nil && b.ProjectID != projectID {
			return nil, ErrBundleMissing
		}
		return b, err
	case !in.ReleaseID.IsZero():
		b, err := lookupBundle(bundleRecord(q.BundleOfRelease(ctx, in.ReleaseID)))
		if b != nil && b.ProjectID != projectID {
			return nil, ErrBundleMissing
		}
		return b, err
	case !in.ReleaseRun.IsZero():
		return s.Bundles.OfRun(ctx, projectID, in.ReleaseRun)
	}
	return nil, nil
}
