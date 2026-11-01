package preview

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/database"
)

// ReleaseSelection names a released version to preview.
type ReleaseSelection struct {
	ProjectID ids.ID
	Number    int64
}

// Release is a released version's recorded launch inputs, never input
// supplied by the user requesting a preview: its bundle, when the release
// was published to a registry (Image names the bundle), or the image it
// recorded on this AppTown's Docker.
type Release struct {
	ProjectID             ids.ID
	Number                int64
	Image, SourceRevision string
	Bundle                *deployment.ReleaseBundle
}

// ReleaseResult is a booted release preview: Image is what it runs, and
// Reused says a running one was handed back.
type ReleaseResult struct {
	Result
	Image  string
	Reused bool
}

// releaseHost boots a selected release's preview under its boot interval.
type releaseHost interface {
	BootReleasePreview(context.Context, Release) (ReleaseResult, error)
}

var (
	errReleasedVersionMissing = fault.New(fault.NotFound, "released version not found")
	errReleaseInputs          = fault.New(fault.Unprocessable, "a recorded image and selected source are required before this release can be previewed")
	// A release not published to a registry is previewed from the image it
	// recorded on this AppTown's own Docker.
	errReleaseNoLocalDocker = fault.New(fault.Unprocessable, "this release was not published to a registry, and this AppTown has no local Docker to run the image it recorded; a release landed since releases are built on the worker can be previewed")
)

// recordedImage is a release's eligibility and a copy of the inputs it
// recorded on this AppTown's Docker. CommitSHA is a release receipt;
// SourceSHA identifies its runnable source tree.
func recordedImage(r *deployment.Release) (Release, error) {
	if r.State != deployment.Released {
		return Release{}, errReleasedVersionMissing
	}
	if r.ExternalRef == "" || r.SourceSHA == "" {
		return Release{}, errReleaseInputs
	}
	return Release{ProjectID: r.ProjectID, Number: r.Number, Image: r.ExternalRef, SourceRevision: r.SourceSHA}, nil
}

// previewOf is what a preview of rel boots: its bundle's images by digest,
// when the release was published to a registry (built on the worker), where
// changes' previews boot; otherwise the image it recorded on this AppTown's
// Docker, which an AppTown with no local Docker does not have.
func (s *Service) previewOf(ctx context.Context, rel *deployment.Release) (Release, error) {
	if rel.State != deployment.Released {
		return Release{}, errReleasedVersionMissing
	}
	if !rel.BundleID.IsZero() {
		b, err := s.bundles.Find(ctx, rel.BundleID)
		if err != nil {
			return Release{}, err
		}
		if b != nil && b.ByDigest() && b.ProjectID == rel.ProjectID {
			return Release{ProjectID: rel.ProjectID, Number: rel.Number, Image: fmt.Sprintf("bundle %d", b.ID), SourceRevision: b.ContractRevision, Bundle: b}, nil
		}
	}
	if s.hosts.NoLocalDocker() {
		return Release{}, errReleaseNoLocalDocker
	}
	return recordedImage(rel)
}

// ReleaseSourceError distinguishes an unavailable native source/contract
// from a provider boot failure without coupling the request to Docker phases.
type ReleaseSourceError struct{ Cause error }

func (e *ReleaseSourceError) Error() string         { return e.Cause.Error() }
func (e *ReleaseSourceError) Unwrap() error         { return e.Cause }
func (e *ReleaseSourceError) FaultKind() fault.Kind { return fault.Unprocessable }

// RequestReleasePreview boots a released version's preview for a person
// who may operate the project's deployments, from what the release
// recorded: never input the request supplies.
func (s *Service) RequestReleasePreview(ctx context.Context, in ReleaseSelection) (out ReleaseResult, err error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.authority == nil {
		return out, errors.New("release previews are not configured")
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentOperate, Resource: access.Resource{Type: "project", ID: in.ProjectID.String(), ProjectID: in.ProjectID}, Context: access.Context{Route: "deployment.release-preview", Consequential: true}}); err != nil {
		return out, err
	}
	if in.ProjectID.IsZero() || in.Number <= 0 {
		return out, errors.New("invalid release preview selection")
	}
	rel, err := s.releases.ByNumber(ctx, in.ProjectID, in.Number)
	found, err := database.Lookup(err)
	if err != nil {
		return out, err
	}
	if !found {
		return out, errReleasedVersionMissing
	}
	selected, err := s.previewOf(ctx, rel)
	if err != nil {
		return out, fmt.Errorf("release %d: %w", in.Number, err)
	}
	boot, err := s.runtime.begin(ctx, releaseKey(in.ProjectID, in.Number))
	if err != nil {
		return out, err
	}
	defer func() { boot.finish(err) }()
	if err = project.RefuseUnlessActive(ctx, s.db, in.ProjectID); err != nil {
		return out, err
	}
	out, err = s.releaseHost.BootReleasePreview(ctx, selected)
	if err != nil {
		return ReleaseResult{}, &ExecutionError{Cause: err}
	}
	out.ProjectID, out.Image = selected.ProjectID, selected.Image
	return out, nil
}
