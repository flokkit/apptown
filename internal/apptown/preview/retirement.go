package preview

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/preview/internal/previewsql"
	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// RemoveRunForGood retires a deleted run's preview, build images, look and
// snapshots. It uses the retained ID even after the run's record is gone.
// Independent local effects are attempted even when the worker fails.
func (s *Service) RemoveRunForGood(ctx context.Context, runID ids.ID) error {
	name := docker.PreviewRunName(runID)
	return errors.Join(s.removePreviewForGood(ctx, name), s.removeLocalPreview(ctx, name),
		os.RemoveAll(s.runSnapshots(runID)))
}

// RemoveRunStepsForGood retires the frozen list of a deleted project's
// preview-step stacks. Worker stacks go with their run's preview; this list
// names the local stacks. Every recorded step is attempted, including on retry.
func (s *Service) RemoveRunStepsForGood(ctx context.Context, stepIDs []ids.ID) error {
	dk := s.hosts.Local()
	if dk.Absent() {
		return nil
	}
	var failed []error
	for _, stepID := range stepIDs {
		failed = append(failed, dk.RemoveDisposableStack(ctx, docker.RunStepStackID(stepID)))
	}
	return errors.Join(failed...)
}

// RemoveChangeForGood retires a deleted change's preview, build images,
// look, disposable stack and snapshots, without reading its deleted record.
func (s *Service) RemoveChangeForGood(ctx context.Context, changeID ids.ID) error {
	name := docker.PreviewChangeName(changeID)
	err := errors.Join(s.removePreviewForGood(ctx, name), s.removeLocalPreview(ctx, name))
	if dk := s.hosts.Local(); !dk.Absent() {
		err = errors.Join(err, dk.RemoveDisposableStack(ctx, docker.ChangeStackID(changeID)))
	}
	return errors.Join(err, os.RemoveAll(s.changeSnapshots(changeID)))
}

func (s *Service) removeLocalPreview(ctx context.Context, name string) error {
	dk := s.hosts.Local()
	if dk.Absent() {
		return nil
	}
	removed := dk.RemoveDisposable(ctx, name)
	// A shared image still in use stays; permanent cleanup never forces it.
	_, images := dk.RemoveBuildImagesOf(ctx, name)
	return errors.Join(removed, images)
}

// RemoveReleaseForGood retires the exact historical preview captured before
// its project and release rows disappeared. It shares boot exclusion, and uses
// the original serving label rather than the project's mutable address.
func (s *Service) RemoveReleaseForGood(ctx context.Context, projectID ids.ID, servingLabel string, number int64) (err error) {
	if projectID.IsZero() || !project.ValidSlug(servingLabel) || number <= 0 {
		return fmt.Errorf("invalid retained release preview identity")
	}
	interval, err := s.runtime.begin(ctx, releaseKey(projectID, number))
	if err != nil {
		return err
	}
	defer func() { interval.finish(err) }()
	name := docker.PreviewLabelName(docker.ReleasePreviewLabel(servingLabel, number))
	defer s.holdPreview(name)()
	s.discardDecided(name)
	err = errors.Join(s.removePreviewForGood(ctx, name), s.removeLocalPreview(ctx, name))
	if dk := s.hosts.Local(); !dk.Absent() {
		err = errors.Join(err, dk.RemoveDisposableStack(ctx, docker.ReleaseStackID(servingLabel, number)))
	}
	if err != nil {
		return err
	}
	// An unavailable observation cannot release a key that may still serve.
	stands, err := s.previewStands(ctx, name)
	if err != nil {
		return err
	}
	if stands {
		return fmt.Errorf("release preview %s still exists after retirement", name)
	}
	rows, err := previewsql.New(s.db.SQL()).PreviewOwnedBy(ctx, previewsql.PreviewOwnedByParams{Preview: name, Kind: deployment.KindOpenRouterKey})
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if row.ProjectID != projectID {
			failures = append(failures, fmt.Errorf("preview %s key belongs to another project", name))
			continue
		}
		failures = append(failures, s.releasePreviewKey(ctx, row))
	}
	return errors.Join(failures...)
}
